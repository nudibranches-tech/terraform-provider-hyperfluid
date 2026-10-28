// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// container_app_workload.go — what the app's containers run: the main
// container's command line and environment, its image pull secrets, and the
// init containers that run before it.

// maxInitContainers matches the CRD's cap.
const maxInitContainers = 4

// containerAppEnvVarModel is one environment variable, set from exactly one of
// a plain value and an organization-wide secret.
type containerAppEnvVarModel struct {
	Name   types.String `tfsdk:"name"`
	Value  types.String `tfsdk:"value"`
	Secret types.String `tfsdk:"secret"`
}

type containerAppInitContainerModel struct {
	Name             types.String `tfsdk:"name"`
	ImageRepository  types.String `tfsdk:"image_repository"`
	ImageTag         types.String `tfsdk:"image_tag"`
	Command          types.List   `tfsdk:"command"`
	Args             types.List   `tfsdk:"args"`
	InheritEnv       types.Bool   `tfsdk:"inherit_env"`
	MountPersistence types.Bool   `tfsdk:"mount_persistence"`
}

type containerAppFileMountModel struct {
	MountPath  types.String `tfsdk:"mount_path"`
	Mode       types.String `tfsdk:"mode"`
	Content    types.String `tfsdk:"content"`
	SecretName types.String `tfsdk:"secret_name"`
}

func containerAppFileMountType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"mount_path":  types.StringType,
		"mode":        types.StringType,
		"content":     types.StringType,
		"secret_name": types.StringType,
	}}
}

// The platform's inline file mount budget, in bytes.
const (
	maxInlineFileBytes  = 8 * 1024
	maxInlineTotalBytes = 32 * 1024
)

func containerAppEnvVarType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":   types.StringType,
		"value":  types.StringType,
		"secret": types.StringType,
	}}
}

func containerAppInitContainerType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":              types.StringType,
		"image_repository":  types.StringType,
		"image_tag":         types.StringType,
		"command":           types.ListType{ElemType: types.StringType},
		"args":              types.ListType{ElemType: types.StringType},
		"inherit_env":       types.BoolType,
		"mount_persistence": types.BoolType,
	}}
}

// workloadLists maps each workload list to its value, keyed by attribute name.
func (m containerAppModel) workloadLists() map[string]types.List {
	return map[string]types.List{
		"env_vars":           m.EnvVars,
		"image_pull_secrets": m.ImagePullSecrets,
		"command":            m.Command,
		"args":               m.Args,
		"init_containers":    m.InitContainers,
		"file_mounts":        m.FileMounts,
	}
}

// unmanagedUnlessSet holds every attribute that is left to the app while the
// configuration has never set it (see unsetList and unsetObject).
func (m containerAppModel) unmanagedUnlessSet() map[string]attr.Value {
	out := map[string]attr.Value{
		"custom_domains": m.CustomDomains,
		"autoscaling":    m.Autoscaling,
		"metrics":        m.Metrics,
	}
	for name, l := range m.workloadLists() {
		out[name] = l
	}
	return out
}

var (
	initContainerName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	notBlank          = regexp.MustCompile(`\S`)
)

func workloadAttributes() map[string]schema.Attribute {
	nonBlank := stringvalidator.RegexMatches(notBlank, "must not be blank")
	return map[string]schema.Attribute{
		"env_vars": schema.ListNestedAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Environment variables of the main container, each set from exactly one of `value` and `secret`. " +
				"A value may reference an earlier plain variable as `$(NAME)`, but not a secret-backed one: those are set after every plain variable.",
			PlanModifiers: []planmodifier.List{unsetList("env_vars")},
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Variable name.",
						Validators: []validator.String{
							stringvalidator.LengthAtLeast(1),
							stringvalidator.RegexMatches(regexp.MustCompile(`^[^=]+$`), "must not contain '='"),
						},
					},
					"value": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Plain-text value, stored in state.",
						Validators: []validator.String{
							stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("secret")),
						},
					},
					"secret": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Name of the organization-wide `hyperfluid_secret` holding the value, which never lands in state.",
						Validators:          []validator.String{stringvalidator.LengthAtLeast(1), orgSecretName{}},
					},
				},
			},
		},
		"image_pull_secrets": schema.ListAttribute{
			ElementType: types.StringType, Optional: true, Computed: true,
			MarkdownDescription: "Names of organization-wide secrets holding registry credentials for pulling the app's images.",
			PlanModifiers:       []planmodifier.List{unsetList("image_pull_secrets")},
			Validators: []validator.List{
				listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1), orgSecretName{}),
			},
		},
		"command": schema.ListAttribute{
			ElementType: types.StringType, Optional: true, Computed: true,
			MarkdownDescription: "Entrypoint, replacing the image's `ENTRYPOINT`; `[]` keeps the image's. " + commandOverrideNote,
			PlanModifiers:       []planmodifier.List{unsetList("command")},
		},
		"args": schema.ListAttribute{
			ElementType: types.StringType, Optional: true, Computed: true,
			MarkdownDescription: "Arguments, replacing the image's `CMD`; `[]` keeps the image's. " + commandOverrideNote,
			PlanModifiers:       []planmodifier.List{unsetList("args")},
		},
		"file_mounts": schema.ListNestedAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Files mounted into the container. Inline content is limited to 8 KiB per file and " +
				"32 KiB in all, and is stored in plain text in state.",
			PlanModifiers: []planmodifier.List{unsetList("file_mounts")},
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"mount_path": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Absolute path of the file, unique in the app; not under `/proc`, `/sys` or `/dev`.",
						Validators:          []validator.String{mountPath{}},
					},
					"mode": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Octal file mode. Defaults to `0644` for `content` and `0400` for `secret_name`.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(regexp.MustCompile(`^0*[0-7]{1,3}$`), "must be an octal file mode of at most 0777, such as 0644"),
						},
					},
					"content": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Content of the file. Exactly one of `content` and `secret_name` is required.",
						Validators: []validator.String{
							stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("secret_name")),
						},
					},
					"secret_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Name of the organization-wide secret whose value is the file.",
						Validators:          []validator.String{stringvalidator.LengthAtLeast(1), orgSecretName{}},
					},
				},
			},
		},
		"init_containers": schema.ListNestedAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Containers run in order before the app starts, at most 4. Setting this replaces " +
				"any init containers an app template added.",
			PlanModifiers: []planmodifier.List{unsetList("init_containers")},
			Validators:    []validator.List{listvalidator.SizeAtMost(maxInitContainers)},
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Required: true,
						MarkdownDescription: "Name, unique in the app and different from its slug: at most 63 lowercase " +
							"letters, digits and hyphens.",
						Validators: []validator.String{
							stringvalidator.LengthBetween(1, 63),
							stringvalidator.RegexMatches(initContainerName,
								"must be lowercase letters, digits and hyphens, not starting or ending with a hyphen"),
						},
					},
					"image_repository": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Image repository, set with `image_tag`. Defaults to the app's own image.",
						Validators: []validator.String{
							nonBlank,
							stringvalidator.AlsoRequires(path.MatchRelative().AtParent().AtName("image_tag")),
						},
					},
					"image_tag": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Image tag, set with `image_repository`.",
						Validators: []validator.String{
							nonBlank,
							stringvalidator.AlsoRequires(path.MatchRelative().AtParent().AtName("image_repository")),
						},
					},
					"command": schema.ListAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Entrypoint, replacing the image's `ENTRYPOINT`. " + commandOverrideNote,
						Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
					},
					"args": schema.ListAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Arguments, replacing the image's `CMD`. " + commandOverrideNote,
						Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
					},
					"inherit_env": schema.BoolAttribute{
						Optional: true, Computed: true, Default: booldefault.StaticBool(true),
						MarkdownDescription: "Whether the container gets the app's environment variables. Defaults to true.",
					},
					"mount_persistence": schema.BoolAttribute{
						Optional: true, Computed: true, Default: booldefault.StaticBool(true),
						MarkdownDescription: "Whether the app's persistent volumes are mounted. Defaults to true.",
					},
				},
			},
		},
	}
}

const commandOverrideNote = "Needs the organization's container command override feature."

// ── organization-wide secret names ────────────────────────────────────────

// isPrivateSecretName reports whether a secret name lies under the private
// root, as `users/<owner>/<leaf>`.
func isPrivateSecretName(name string) bool {
	return name == "users" || strings.HasPrefix(name, "users/")
}

// orgSecretName refuses a private secret: a container app's references are
// resolved by name when the app is deployed, where only organization-wide
// secrets exist, so the API refuses them too.
type orgSecretName struct{}

func (orgSecretName) Description(context.Context) string {
	return "the name of an organization-wide secret, not a private one under users/"
}

func (v orgSecretName) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (orgSecretName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if isPrivateSecretName(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Private secret",
			fmt.Sprintf("%q is a private secret. A container app reads its secrets when it is deployed, where only "+
				"organization-wide secrets are visible: use one of those instead.", req.ConfigValue.ValueString()))
	}
}

// ── lists the configuration leaves out ────────────────────────────────────

// managedKey is the private-state key recording which of the attributes an app
// may also get from the console or a template the configuration set at the
// last apply.
const managedKey = "managed_attributes"

type privateGetter interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateSetter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

func managedAttributes(ctx context.Context, p privateGetter) (map[string]bool, diag.Diagnostics) {
	raw, d := p.GetKey(ctx, managedKey)
	if d.HasError() || len(raw) == 0 {
		return nil, d
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		d.AddError("Corrupt private state", "the "+managedKey+" entry is not a list of names: "+err.Error())
		return nil, d
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, d
}

// recordManagedAttributes stores which of those attributes cfg sets, for the
// next plan.
func recordManagedAttributes(ctx context.Context, p privateSetter, cfg containerAppModel) diag.Diagnostics {
	return recordManaged(ctx, p, cfg.unmanagedUnlessSet())
}

// recordManaged stores which of these configured values are set.
func recordManaged(ctx context.Context, p privateSetter, values map[string]attr.Value) diag.Diagnostics {
	names := []string{}
	for name, v := range values {
		if !v.IsNull() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	raw, err := json.Marshal(names)
	if err != nil {
		var d diag.Diagnostics
		d.AddError("Failed to record private state", err.Error())
		return d
	}
	return p.SetKey(ctx, managedKey, raw)
}

// unsetList plans a workload list the configuration leaves out (see
// plannedUnsetList).
func unsetList(attribute string) planmodifier.List { return unsetListModifier{attribute: attribute} }

type unsetListModifier struct{ attribute string }

func (unsetListModifier) Description(context.Context) string {
	return "Keeps the app's value when the configuration never set it, and clears it once the configuration stops setting it."
}

func (m unsetListModifier) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m unsetListModifier) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	// A new app reads the value back after create, so it stays unknown.
	if !req.ConfigValue.IsNull() || req.StateValue.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	managed, d := managedAttributes(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	resp.PlanValue = plannedUnsetList(req.StateValue, managed[m.attribute])
}

// unsetObject is unsetList for a single nested object, planned null instead
// of empty once Terraform stops setting it.
func unsetObject(attribute string) planmodifier.Object {
	return unsetObjectModifier{attribute: attribute}
}

type unsetObjectModifier struct{ attribute string }

func (unsetObjectModifier) Description(context.Context) string {
	return "Keeps the app's value when the configuration never set it, and removes it once the configuration stops setting it."
}

func (m unsetObjectModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m unsetObjectModifier) PlanModifyObject(ctx context.Context, req planmodifier.ObjectRequest, resp *planmodifier.ObjectResponse) {
	if !req.ConfigValue.IsNull() || req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	managed, d := managedAttributes(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	resp.PlanValue = plannedUnsetObject(req.StateValue, managed[m.attribute])
}

func plannedUnsetObject(state types.Object, managed bool) types.Object {
	if managed {
		return types.ObjectNull(state.AttributeTypes(context.Background()))
	}
	return state
}

// plannedUnsetList is the plan for a list missing from the configuration: what
// the app has when Terraform never set it, empty when Terraform set it before.
func plannedUnsetList(state types.List, managed bool) types.List {
	if managed {
		return types.ListValueMust(state.ElementType(context.Background()), []attr.Value{})
	}
	return state
}

// ── request bodies ────────────────────────────────────────────────────────

// workloadInputs holds the workload fields of a create or patch body. A nil
// field is left out, which the API reads as "keep what is there".
type workloadInputs struct {
	FileMounts       *[]console.FileMountInput
	Env              *[]console.EnvVarInput
	SecretRefs       *[]console.SecretRefInput
	ImagePullSecrets *[]string
	Command          *[]string
	Args             *[]string
	InitContainers   *[]console.InitContainerInput
}

// workloadInputsFor builds the fields to send. On create (prior nil) a list is
// sent when it is known; on update only when it differs from prior, so an
// unchanged command is never re-sent to an organization that has since lost
// the override feature.
func workloadInputsFor(ctx context.Context, plan containerAppModel, prior *containerAppModel) (workloadInputs, diag.Diagnostics) {
	var (
		in    workloadInputs
		diags diag.Diagnostics
	)
	send := func(p, s types.List) bool {
		if p.IsNull() || p.IsUnknown() {
			return false
		}
		return prior == nil || !p.Equal(s)
	}
	var priorLists map[string]types.List
	if prior != nil {
		priorLists = prior.workloadLists()
	}

	// The API keeps plain and secret-backed variables in separate lists, each
	// replaced on its own, so a change to env_vars sends both.
	if send(plan.EnvVars, priorLists["env_vars"]) {
		var models []containerAppEnvVarModel
		diags.Append(plan.EnvVars.ElementsAs(ctx, &models, false)...)
		env := make([]console.EnvVarInput, 0, len(models))
		refs := make([]console.SecretRefInput, 0, len(models))
		for _, m := range models {
			if m.Secret.IsNull() {
				env = append(env, console.EnvVarInput{Name: m.Name.ValueString(), Value: m.Value.ValueString()})
			} else {
				refs = append(refs, console.SecretRefInput{EnvVarName: m.Name.ValueString(), SecretName: m.Secret.ValueString()})
			}
		}
		in.Env, in.SecretRefs = &env, &refs
	}
	stringsOf := func(l types.List) *[]string {
		out := []string{}
		diags.Append(l.ElementsAs(ctx, &out, false)...)
		return &out
	}
	if send(plan.ImagePullSecrets, priorLists["image_pull_secrets"]) {
		in.ImagePullSecrets = stringsOf(plan.ImagePullSecrets)
	}
	if send(plan.Command, priorLists["command"]) {
		in.Command = stringsOf(plan.Command)
	}
	if send(plan.Args, priorLists["args"]) {
		in.Args = stringsOf(plan.Args)
	}
	if send(plan.InitContainers, priorLists["init_containers"]) {
		var models []containerAppInitContainerModel
		diags.Append(plan.InitContainers.ElementsAs(ctx, &models, false)...)
		ics := make([]console.InitContainerInput, 0, len(models))
		for _, m := range models {
			ic := console.InitContainerInput{
				Name:             m.Name.ValueString(),
				ImageRepository:  stringPtr(m.ImageRepository),
				ImageTag:         stringPtr(m.ImageTag),
				InheritEnv:       boolPtr(m.InheritEnv),
				MountPersistence: boolPtr(m.MountPersistence),
			}
			if !m.Command.IsNull() && !m.Command.IsUnknown() {
				ic.Command = stringsOf(m.Command)
			}
			if !m.Args.IsNull() && !m.Args.IsUnknown() {
				ic.Args = stringsOf(m.Args)
			}
			ics = append(ics, ic)
		}
		in.InitContainers = &ics
	}
	if send(plan.FileMounts, priorLists["file_mounts"]) {
		var models []containerAppFileMountModel
		diags.Append(plan.FileMounts.ElementsAs(ctx, &models, false)...)
		mounts := make([]console.FileMountInput, 0, len(models))
		for _, m := range models {
			fm := console.FileMountInput{MountPath: m.MountPath.ValueString(), Mode: stringPtr(m.Mode)}
			var err error
			if m.SecretName.IsNull() {
				err = fm.Source.FromFileMountSourceInput0(console.FileMountSourceInput0{
					Type: console.FileMountSourceInput0TypeInline, Content: m.Content.ValueString(),
				})
			} else {
				err = fm.Source.FromFileMountSourceInput1(console.FileMountSourceInput1{
					Type: console.FileMountSourceInput1TypeSecretManager, SecretName: m.SecretName.ValueString(),
				})
			}
			if err != nil {
				diags.AddAttributeError(path.Root("file_mounts"), "Invalid file mount", err.Error())
			}
			mounts = append(mounts, fm)
		}
		in.FileMounts = &mounts
	}
	return in, diags
}

// setsCommandOverride reports whether the inputs set a non-empty command or
// args anywhere, which the platform gates per organization.
func (in workloadInputs) setsCommandOverride() bool {
	nonEmpty := func(s *[]string) bool { return s != nil && len(*s) > 0 }
	if nonEmpty(in.Command) || nonEmpty(in.Args) {
		return true
	}
	if in.InitContainers != nil {
		for _, ic := range *in.InitContainers {
			if nonEmpty(ic.Command) || nonEmpty(ic.Args) {
				return true
			}
		}
	}
	return false
}

// addWriteError reports a failed create or patch, explaining the 403 the
// platform answers when the organization may not override a command.
func addWriteError(diags *diag.Diagnostics, summary string, in workloadInputs, err error) {
	if errors.Is(err, client.ErrForbidden) && in.setsCommandOverride() &&
		strings.Contains(strings.ToLower(err.Error()), "command override") {
		diags.AddError("Command override not enabled",
			"This organization does not have the container command override feature, so the platform refuses a "+
				"new or changed non-empty command or args, on the app or on an init container (an init container "+
				"that keeps its name, command and args is accepted). Ask a platform administrator to enable it, or "+
				"remove command and args from the configuration to run the images' own entrypoints.\n\n"+
				err.Error())
		return
	}
	diags.AddError(summary, err.Error())
}

// ── reading back ──────────────────────────────────────────────────────────

type workloadState struct {
	EnvVars, ImagePullSecrets, Command, Args, InitContainers, FileMounts types.List
}

// workloadFrom maps the spec's workload onto known lists, empty when the app
// has none. priorEnv is the env_vars the read follows.
func workloadFrom(ctx context.Context, spec *console.ContainerAppCrdSpecResponse, priorEnv types.List) (workloadState, diag.Diagnostics) {
	var (
		w     workloadState
		diags diag.Diagnostics
		d     diag.Diagnostics
	)
	env := make([]containerAppEnvVarModel, 0, len(spec.Env)+len(spec.SecretRefs))
	for _, e := range spec.Env {
		env = append(env, containerAppEnvVarModel{
			Name: types.StringValue(e.Name), Value: types.StringValue(e.Value), Secret: types.StringNull(),
		})
	}
	for _, s := range spec.SecretRefs {
		env = append(env, containerAppEnvVarModel{
			Name: types.StringValue(s.EnvVarName), Value: types.StringNull(), Secret: types.StringValue(s.SecretName),
		})
	}
	w.EnvVars, d = types.ListValueFrom(ctx, containerAppEnvVarType(), inPriorOrder(ctx, env, priorEnv))
	diags.Append(d...)

	w.ImagePullSecrets = stringsList(ctx, spec.ImagePullSecrets, &diags)
	w.Command = stringsList(ctx, deref(spec.Command), &diags)
	w.Args = stringsList(ctx, deref(spec.Args), &diags)

	ics := make([]containerAppInitContainerModel, 0)
	for _, ic := range deref(spec.InitContainers) {
		ics = append(ics, containerAppInitContainerModel{
			Name:             types.StringValue(ic.Name),
			ImageRepository:  optString(ic.ImageRepository),
			ImageTag:         optString(ic.ImageTag),
			Command:          nullIfEmpty(ctx, ic.Command, &diags),
			Args:             nullIfEmpty(ctx, ic.Args, &diags),
			InheritEnv:       types.BoolValue(ic.InheritEnv),
			MountPersistence: types.BoolValue(ic.MountPersistence),
		})
	}
	w.InitContainers, d = types.ListValueFrom(ctx, containerAppInitContainerType(), ics)
	diags.Append(d...)

	mounts := make([]containerAppFileMountModel, 0, len(spec.FileMounts))
	for i, fm := range spec.FileMounts {
		m := containerAppFileMountModel{
			MountPath: types.StringValue(fm.MountPath), Mode: optString(fm.Mode),
			Content: types.StringNull(), SecretName: types.StringNull(),
		}
		if secret, err := fm.Source.AsFileMountSourceResponse1(); err == nil && secret.Type == console.FileMountSourceResponse1TypeSecretManager {
			m.SecretName = types.StringValue(secret.SecretName)
		} else if inline, err := fm.Source.AsFileMountSourceResponse0(); err == nil && inline.Type == console.FileMountSourceResponse0TypeInline {
			m.Content = types.StringValue(inline.Content)
		} else {
			diags.AddError("Unknown file mount source", fmt.Sprintf("file_mounts[%d] has a source this provider does not know", i))
		}
		mounts = append(mounts, m)
	}
	w.FileMounts, d = types.ListValueFrom(ctx, containerAppFileMountType(), mounts)
	diags.Append(d...)
	return w, diags
}

// inPriorOrder returns read in the order of prior when both hold the same
// variables. The API lists plain variables before secret-backed ones, which
// would otherwise show a diff for a configuration that interleaves them.
func inPriorOrder(ctx context.Context, read []containerAppEnvVarModel, prior types.List) []containerAppEnvVarModel {
	if prior.IsNull() || prior.IsUnknown() || len(prior.Elements()) != len(read) {
		return read
	}
	var priorModels []containerAppEnvVarModel
	if d := prior.ElementsAs(ctx, &priorModels, false); d.HasError() {
		return read
	}
	byName := make(map[string]containerAppEnvVarModel, len(read))
	for _, e := range read {
		byName[e.Name.ValueString()] = e
	}
	out := make([]containerAppEnvVarModel, 0, len(read))
	for _, p := range priorModels {
		e, ok := byName[p.Name.ValueString()]
		if !ok || !e.Value.Equal(p.Value) || !e.Secret.Equal(p.Secret) {
			return read
		}
		delete(byName, p.Name.ValueString())
		out = append(out, e)
	}
	return out
}

func deref[T any](s *[]T) []T {
	if s == nil {
		return nil
	}
	return *s
}

// stringsList is a known list of strings, empty for a nil slice.
func stringsList(ctx context.Context, s []string, diags *diag.Diagnostics) types.List {
	if s == nil {
		s = []string{}
	}
	l, d := types.ListValueFrom(ctx, types.StringType, s)
	diags.Append(d...)
	return l
}

// nullIfEmpty maps an init container's empty command or args to null: the API
// reports an unset one as empty, and the schema refuses an empty list.
func nullIfEmpty(ctx context.Context, s []string, diags *diag.Diagnostics) types.List {
	if len(s) == 0 {
		return types.ListNull(types.StringType)
	}
	return stringsList(ctx, s, diags)
}

// ── plan-time validation ──────────────────────────────────────────────────

// validateWorkload applies the API's cross-entry rules at plan time: unique
// names, an app slug no init container may take, and a non-blank executable.
func validateWorkload(ctx context.Context, cfg containerAppModel, diags *diag.Diagnostics) {
	validateCommand(ctx, cfg.Command, path.Root("command"), diags)
	validateEnvVars(ctx, cfg.EnvVars, diags)
	validateFileMounts(ctx, cfg.FileMounts, diags)

	if cfg.InitContainers.IsNull() || cfg.InitContainers.IsUnknown() {
		return
	}
	var ics []containerAppInitContainerModel
	diags.Append(cfg.InitContainers.ElementsAs(ctx, &ics, false)...)
	slug := cfg.Slug
	if slug.IsNull() {
		slug = cfg.Name
	}
	names := map[string]bool{}
	for i, ic := range ics {
		at := path.Root("init_containers").AtListIndex(i)
		validateCommand(ctx, ic.Command, at.AtName("command"), diags)
		if ic.Name.IsNull() || ic.Name.IsUnknown() {
			continue
		}
		name := ic.Name.ValueString()
		if !slug.IsNull() && !slug.IsUnknown() && name == slug.ValueString() {
			diags.AddAttributeError(at.AtName("name"), "Init container named after the app",
				fmt.Sprintf("%q is the app's slug, which names its main container. Choose another name.", name))
		}
		if names[name] {
			diags.AddAttributeError(at.AtName("name"), "Duplicate init container",
				fmt.Sprintf("Another init container is already named %q.", name))
		}
		names[name] = true
	}
}

// validateEnvVars refuses a variable set twice, and a plain value referencing a
// secret-backed variable: the platform sets those after every plain variable,
// so Kubernetes would leave the reference unexpanded.
func validateEnvVars(ctx context.Context, l types.List, diags *diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return
	}
	var env []containerAppEnvVarModel
	diags.Append(l.ElementsAs(ctx, &env, false)...)
	seen, secretBacked := map[string]bool{}, map[string]bool{}
	for i, e := range env {
		if e.Name.IsNull() || e.Name.IsUnknown() {
			continue
		}
		name := e.Name.ValueString()
		if seen[name] {
			diags.AddAttributeError(path.Root("env_vars").AtListIndex(i).AtName("name"), "Duplicate environment variable",
				fmt.Sprintf("Another entry already sets %q.", name))
		}
		seen[name] = true
		if !e.Secret.IsNull() {
			secretBacked[name] = true
		}
	}
	for i, e := range env {
		if e.Value.IsNull() || e.Value.IsUnknown() {
			continue
		}
		for _, ref := range envReferences(e.Value.ValueString()) {
			if secretBacked[ref] {
				diags.AddAttributeError(path.Root("env_vars").AtListIndex(i).AtName("value"), "Reference to a secret-backed variable",
					fmt.Sprintf("$(%s) names a variable set from a secret. Secret-backed variables are set after every plain "+
						"one, so the reference would stay unexpanded.", ref))
			}
		}
	}
}

// envReferences lists the variables a value references as $(NAME), skipping
// the escaped form $$(NAME).
func envReferences(value string) []string {
	var refs []string
	for i := 0; i+1 < len(value); i++ {
		if value[i] != '$' {
			continue
		}
		if value[i+1] == '$' {
			i++
			continue
		}
		if value[i+1] != '(' {
			continue
		}
		end := strings.IndexByte(value[i+2:], ')')
		if end < 0 {
			break
		}
		refs = append(refs, value[i+2:i+2+end])
		i += 2 + end
	}
	return refs
}

// validateCommand refuses a blank executable, which would only fail once the
// container starts. Later tokens may be blank on purpose (`sh -c ""`).
func validateCommand(ctx context.Context, l types.List, at path.Path, diags *diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return
	}
	var tokens []types.String
	diags.Append(l.ElementsAs(ctx, &tokens, false)...)
	if len(tokens) == 0 || tokens[0].IsUnknown() {
		return
	}
	if strings.TrimSpace(tokens[0].ValueString()) == "" {
		diags.AddAttributeError(at.AtListIndex(0), "Blank executable",
			"The first element of command is the executable to run and cannot be blank. Leave command out to use the image's entrypoint.")
	}
}

// validateFileMounts applies the platform's size budget and refuses two files
// at one path.
func validateFileMounts(ctx context.Context, l types.List, diags *diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return
	}
	var mounts []containerAppFileMountModel
	diags.Append(l.ElementsAs(ctx, &mounts, false)...)
	total, paths := 0, map[string]bool{}
	for i, m := range mounts {
		at := path.Root("file_mounts").AtListIndex(i)
		if !m.Content.IsNull() && !m.Content.IsUnknown() {
			n := len(m.Content.ValueString())
			if n > maxInlineFileBytes {
				diags.AddAttributeError(at.AtName("content"), "File too large",
					fmt.Sprintf("The content is %d bytes; an inline file holds at most %d. Use secret_name, or put the file in the image.", n, maxInlineFileBytes))
			}
			total += n
		}
		if m.MountPath.IsNull() || m.MountPath.IsUnknown() {
			continue
		}
		if paths[m.MountPath.ValueString()] {
			diags.AddAttributeError(at.AtName("mount_path"), "Duplicate file mount",
				fmt.Sprintf("Another file is already mounted at %q.", m.MountPath.ValueString()))
		}
		paths[m.MountPath.ValueString()] = true
	}
	if total > maxInlineTotalBytes {
		diags.AddAttributeError(path.Root("file_mounts"), "File mounts too large",
			fmt.Sprintf("Inline content totals %d bytes; an app holds at most %d across its files.", total, maxInlineTotalBytes))
	}
}

// mountPath refuses the paths the platform does not mount a file at.
type mountPath struct{}

func (mountPath) Description(context.Context) string {
	return "an absolute, canonical path, not / and not under /proc, /sys or /dev"
}

func (v mountPath) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (mountPath) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	p := req.ConfigValue.ValueString()
	var problem string
	switch {
	case !strings.HasPrefix(p, "/"):
		problem = "must be absolute (start with /)"
	case p == "/":
		problem = "cannot be /"
	case strings.Contains(p, "//") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/.."):
		problem = "must be canonical, without // or .. segments"
	default:
		for _, dir := range []string{"/proc", "/sys", "/dev"} {
			if p == dir || strings.HasPrefix(p, dir+"/") {
				problem = "cannot be under " + dir
			}
		}
	}
	if problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid mount path", fmt.Sprintf("%q %s.", p, problem))
	}
}

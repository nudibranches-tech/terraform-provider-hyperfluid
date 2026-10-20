// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// rabbitmq_resource.go — managed RabbitMQ broker (ADR-0055). Connection is
// surfaced via host/ports + the secret-manager paths of the credentials the
// platform generates (never the values).
//
// `image`, `replicas`, `storage` and `resources` are pinned only when set:
// omitted, they stay null in state and the platform resolves its default at
// reconcile time, so a bump of that default reaches the broker.

var (
	_ resource.Resource                = &rabbitMQResource{}
	_ resource.ResourceWithConfigure   = &rabbitMQResource{}
	_ resource.ResourceWithImportState = &rabbitMQResource{}
)

// A three- or five-node cluster forms one node at a time, so this is wider than
// the key-value cache's.
const rabbitMQWaitTimeout = 15 * time.Minute

// rabbitMQPlugins is the platform's plugin allowlist (`RabbitMqPlugin`).
var rabbitMQPlugins = []string{
	string(console.RabbitMqPluginStream),
	string(console.RabbitMqPluginShovel),
	string(console.RabbitMqPluginFederation),
	string(console.RabbitMqPluginMqtt),
}

// Mirrors the HfRabbitMq CRD's CEL rules.
var (
	rabbitMQNameRe    = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)
	rabbitMQStorageRe = regexp.MustCompile(`^[1-9][0-9]*(Ki|Mi|Gi|Ti|Pi|k|M|G|T|P)?$`)
)

const rabbitMQNameMaxLen = 59

func NewRabbitMQResource() resource.Resource {
	return &rabbitMQResource{}
}

type rabbitMQResource struct {
	p *providerData
}

type rabbitMQModel struct {
	ID          types.String `tfsdk:"id"`
	Env         types.String `tfsdk:"env"`
	Name        types.String `tfsdk:"name"`
	Replicas    types.Int64  `tfsdk:"replicas"`
	Storage     types.String `tfsdk:"storage"`
	Resources   types.Object `tfsdk:"resources"`
	Plugins     types.Set    `tfsdk:"plugins"`
	Image       types.String `tfsdk:"image"`
	Description types.String `tfsdk:"description"`
	Tags        types.List   `tfsdk:"tags"`

	// computed
	Host                    types.String `tfsdk:"host"`
	AmqpPort                types.Int64  `tfsdk:"amqp_port"`
	ManagementPort          types.Int64  `tfsdk:"management_port"`
	Phase                   types.String `tfsdk:"phase"`
	Slug                    types.String `tfsdk:"slug"`
	UsernameSecretName      types.String `tfsdk:"username_secret_name"`
	PasswordSecretName      types.String `tfsdk:"password_secret_name"`
	AmqpURISecretName       types.String `tfsdk:"amqp_uri_secret_name"`
	ManagementURISecretName types.String `tfsdk:"management_uri_secret_name"`
}

var (
	rabbitMQQuantityAttrTypes = map[string]attr.Type{
		"cpu":    types.StringType,
		"memory": types.StringType,
	}
	rabbitMQResourcesAttrTypes = map[string]attr.Type{
		"requests": types.ObjectType{AttrTypes: rabbitMQQuantityAttrTypes},
		"limits":   types.ObjectType{AttrTypes: rabbitMQQuantityAttrTypes},
	}
)

func (r *rabbitMQResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rabbitmq"
}

func (r *rabbitMQResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	// Stable for the life of the broker: derived from the environment and the
	// name, which both force a new broker.
	stableStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			Computed: true, MarkdownDescription: desc,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
	}
	quantity := func(desc string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{
			Optional:            true,
			MarkdownDescription: desc,
			Attributes: map[string]schema.Attribute{
				"cpu":    schema.StringAttribute{Optional: true, MarkdownDescription: "CPU quantity, e.g. `\"500m\"` or `\"1\"`."},
				"memory": schema.StringAttribute{Optional: true, MarkdownDescription: "Memory quantity, e.g. `\"1Gi\"`."},
			},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed RabbitMQ broker. The platform generates its credentials and stores them in the secret manager; " +
			"this resource only reports where they are (the `*_secret_name` attributes), never their values.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id the broker runs in. Changing this forces a new broker.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: fmt.Sprintf("Broker name (RFC 1035 label, at most %d characters). Changing this forces a new broker.",
					rabbitMQNameMaxLen),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthAtMost(rabbitMQNameMaxLen),
					stringvalidator.RegexMatches(rabbitMQNameRe,
						"must be an RFC 1035 label: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit"),
				},
			},
			"replicas": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Number of broker nodes: `1`, `3` or `5`. Can only grow: a RabbitMQ cluster cannot be scaled down, " +
					"so a lower value is rejected at plan time. Omitted, the platform default applies and is never written into state; " +
					"removing it after it was set keeps the current node count.",
				Validators: []validator.Int64{int64validator.OneOf(1, 3, 5)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					rabbitMQReplicasNoDecrease{},
				},
			},
			"storage": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Volume size per node, a whole Kubernetes quantity such as `\"20Gi\"`. Can only grow: a smaller value " +
					"is rejected at plan time. Omitted, the platform default applies and is never written into state; removing it after " +
					"it was set keeps the current size.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(rabbitMQStorageRe, "must be a whole Kubernetes quantity, e.g. \"20Gi\""),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					rabbitMQStorageNoShrink{},
				},
			},
			"resources": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "CPU and memory requests and limits per node. Omitted, the platform default applies. " +
					"Leaving `limits.cpu` unset avoids CPU throttling.",
				Attributes: map[string]schema.Attribute{
					"requests": quantity("Resource requests."),
					"limits":   quantity("Resource limits."),
				},
				PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
			},
			"plugins": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "Optional plugins to enable, from the allowlist: `" + strings.Join(rabbitMQPlugins, "`, `") + "`. " +
					"The management, Prometheus and Kubernetes peer-discovery plugins are always on. Defaults to none.",
				Default:    setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})),
				Validators: []validator.Set{setvalidator.ValueStringsAre(stringvalidator.OneOf(rabbitMQPlugins...))},
			},
			"image": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "RabbitMQ container image. Omitted, the broker tracks the platform-pinned image, resolved at reconcile time, " +
					"and this stays null; removing it after it was set keeps the current pin.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Free-form description."},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "User-defined tags.",
			},

			"host":                       computedStr("Broker host, once provisioned."),
			"amqp_port":                  schema.Int64Attribute{Computed: true, MarkdownDescription: "AMQP port, once provisioned."},
			"management_port":            schema.Int64Attribute{Computed: true, MarkdownDescription: "Management (HTTP API) port, once provisioned."},
			"phase":                      computedStr("Current lifecycle phase."),
			"slug":                       stableStr("Derived slug."),
			"username_secret_name":       stableStr("Secret-manager name of the broker username (reference it with `secret:<name>`)."),
			"password_secret_name":       stableStr("Secret-manager name of the broker password (reference it with `secret:<name>`)."),
			"amqp_uri_secret_name":       stableStr("Secret-manager name of the AMQP connection URI, credentials included (reference it with `secret:<name>`)."),
			"management_uri_secret_name": stableStr("Secret-manager name of the management API URI, credentials included (reference it with `secret:<name>`)."),
		},
	}
}

func (r *rabbitMQResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	r.p = pd
}

func (r *rabbitMQResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rabbitMQModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags, d := listToStringSlice(ctx, plan.Tags)
	resp.Diagnostics.Append(d...)
	plugins, d := rabbitMQPluginsFrom(ctx, plan.Plugins)
	resp.Diagnostics.Append(d...)
	resources, d := rabbitMQResourcesFrom(ctx, plan.Resources)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := console.CreateHfRabbitMqCrdRequestBody{
		Name:        plan.Name.ValueString(),
		Replicas:    int32PtrFromInt64(plan.Replicas),
		Storage:     stringPtr(plan.Storage),
		Resources:   resources,
		Plugins:     plugins,
		Image:       stringPtr(plan.Image),
		Description: stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}

	created, err := r.p.API.CreateRabbitMQ(ctx, r.p.OrgID, plan.Env.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create RabbitMQ broker", err.Error())
		return
	}
	id := created.Id.String()

	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Broker did not become ready", err.Error())
		return
	}
	state, err := r.readInto(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read broker after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *rabbitMQResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior rabbitMQModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read RabbitMQ broker", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *rabbitMQResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state rabbitMQModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags, d := listToStringSlice(ctx, plan.Tags)
	resp.Diagnostics.Append(d...)
	plugins, d := rabbitMQPluginsFrom(ctx, plan.Plugins)
	resp.Diagnostics.Append(d...)
	resources, d := rabbitMQResourcesFrom(ctx, plan.Resources)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A null field is left out of the PATCH, which keeps the broker's current
	// value: the API cannot unpin a field once set.
	body := console.PatchHfRabbitMqCrdRequestBody{
		Replicas:    int32PtrFromInt64(plan.Replicas),
		Storage:     stringPtr(plan.Storage),
		Resources:   resources,
		Plugins:     plugins,
		Image:       stringPtr(plan.Image),
		Description: stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}

	id := state.ID.ValueString()
	if err := r.p.API.PatchRabbitMQ(ctx, r.p.OrgID, id, body); err != nil {
		resp.Diagnostics.AddError("Failed to update RabbitMQ broker", err.Error())
		return
	}
	newState, err := r.readInto(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read broker after update", err.Error())
		return
	}
	// In-place fields reflect the plan (the operator converges): a scale-up or
	// a volume expansion is eventually consistent, so the read can still report
	// the old value right after PATCH. Computed fields come from the read.
	if !plan.Replicas.IsUnknown() && !plan.Replicas.IsNull() {
		newState.Replicas = plan.Replicas
	}
	if !plan.Storage.IsUnknown() && !plan.Storage.IsNull() {
		newState.Storage = plan.Storage
	}
	if !plan.Resources.IsUnknown() && !plan.Resources.IsNull() {
		newState.Resources = plan.Resources
	}
	if !plan.Image.IsUnknown() && !plan.Image.IsNull() {
		newState.Image = plan.Image
	}
	if !plan.Plugins.IsUnknown() {
		newState.Plugins = plan.Plugins
	}
	if !plan.Tags.IsUnknown() {
		newState.Tags = plan.Tags
	}
	newState.Description = plan.Description
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *rabbitMQResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rabbitMQModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteRabbitMQ(ctx, r.p.OrgID, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete RabbitMQ broker", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, rabbitMQWaitTimeout, func() error {
		_, err := r.p.API.GetRabbitMQ(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Broker still present after delete", err.Error())
	}
}

func (r *rabbitMQResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *rabbitMQResource) waitReady(ctx context.Context, id string) error {
	_, err := waitForReady(ctx, rabbitMQWaitTimeout, func() (*console.HfRabbitMqResponse, bool, error) {
		b, err := r.p.API.GetRabbitMQ(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		ready, err := rabbitMQReady(b)
		return b, ready, err
	})
	return err
}

// rabbitMQReady reports whether a broker is ready, and turns a `Failed` phase
// into an error so the apply ends at once instead of burning the timeout.
func rabbitMQReady(b *console.HfRabbitMqResponse) (bool, error) {
	phase := ""
	if b.Phase != nil {
		phase = *b.Phase
	}
	switch {
	case strings.EqualFold(phase, "Failed"):
		msg := "broker is in phase Failed"
		if b.Message != nil && *b.Message != "" {
			msg += ": " + *b.Message
		}
		return false, errors.New(msg)
	case strings.EqualFold(phase, "Ready"):
		// connection info populated == provisioned and reachable.
		return b.Host != nil && b.AmqpPort != nil, nil
	default:
		return false, nil
	}
}

func (r *rabbitMQResource) readInto(ctx context.Context, id string) (rabbitMQModel, error) {
	b, err := r.p.API.GetRabbitMQ(ctx, r.p.OrgID, id)
	if err != nil {
		return rabbitMQModel{}, err
	}
	// `resources` is only carried by the CRD spec view.
	spec, err := r.p.API.GetRabbitMQSpec(ctx, r.p.OrgID, id)
	if err != nil {
		return rabbitMQModel{}, err
	}
	envID := b.HarborId.String()
	envSlug, err := r.p.API.FindEnvSlug(ctx, r.p.OrgID, envID)
	if err != nil {
		return rabbitMQModel{}, fmt.Errorf("resolve environment %s: %w", envID, err)
	}

	tags, d := stringSliceToList(ctx, b.Tags)
	if d.HasError() {
		return rabbitMQModel{}, errors.New("failed to convert tags")
	}
	plugins, d := rabbitMQPluginsToSet(ctx, b.Plugins)
	if d.HasError() {
		return rabbitMQModel{}, errors.New("failed to convert plugins")
	}
	resources, d := rabbitMQResourcesToObject(spec.Resources)
	if d.HasError() {
		return rabbitMQModel{}, errors.New("failed to convert resources")
	}
	secrets := rabbitMQSecretNames(envSlug, b.Slug)

	return rabbitMQModel{
		ID:                      types.StringValue(id),
		Env:                     types.StringValue(envID),
		Name:                    types.StringValue(b.Name),
		Replicas:                optInt64FromInt32(b.Replicas),
		Storage:                 optString(b.Storage),
		Resources:               resources,
		Plugins:                 plugins,
		Image:                   optString(b.Image),
		Description:             optString(b.Description),
		Tags:                    tags,
		Host:                    optString(b.Host),
		AmqpPort:                optInt64FromInt32(b.AmqpPort),
		ManagementPort:          optInt64FromInt32(b.ManagementPort),
		Phase:                   optString(b.Phase),
		Slug:                    types.StringValue(b.Slug),
		UsernameSecretName:      types.StringValue(secrets.username),
		PasswordSecretName:      types.StringValue(secrets.password),
		AmqpURISecretName:       types.StringValue(secrets.amqpURI),
		ManagementURISecretName: types.StringValue(secrets.managementURI),
	}, nil
}

type rabbitMQSecrets struct {
	username, password, amqpURI, managementURI string
}

// rabbitMQSecretNames returns the secret-manager names the operator publishes a
// broker's credentials under: `<env-slug>/hf-rabbitmq/<broker-slug>/<field>`.
func rabbitMQSecretNames(envSlug, brokerSlug string) rabbitMQSecrets {
	base := envSlug + "/hf-rabbitmq/" + brokerSlug + "/"
	return rabbitMQSecrets{
		username:      base + "username",
		password:      base + "password",
		amqpURI:       base + "amqp-uri",
		managementURI: base + "management-uri",
	}
}

// rabbitMQPluginsFrom turns the plugins set into the request field. A known set
// is always sent, empty included, so removing every plugin disables them.
func rabbitMQPluginsFrom(ctx context.Context, s types.Set) (*[]console.RabbitMqPlugin, diag.Diagnostics) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	names, d := setToStringSlice(ctx, s)
	if d.HasError() {
		return nil, d
	}
	out := make([]console.RabbitMqPlugin, 0, len(names))
	for _, n := range names {
		out = append(out, console.RabbitMqPlugin(n))
	}
	return &out, d
}

func rabbitMQPluginsToSet(ctx context.Context, plugins []console.RabbitMqPlugin) (types.Set, diag.Diagnostics) {
	names := make([]string, 0, len(plugins))
	for _, p := range plugins {
		names = append(names, string(p))
	}
	return types.SetValueFrom(ctx, types.StringType, names)
}

func rabbitMQResourcesFrom(ctx context.Context, o types.Object) (*console.ResourceRequirementsSpec, diag.Diagnostics) {
	if o.IsNull() || o.IsUnknown() {
		return nil, nil
	}
	var diags diag.Diagnostics
	quantity := func(name string) *console.ResourceSpec {
		v, ok := o.Attributes()[name].(basetypes.ObjectValue)
		if !ok || v.IsNull() || v.IsUnknown() {
			return nil
		}
		var q struct {
			CPU    types.String `tfsdk:"cpu"`
			Memory types.String `tfsdk:"memory"`
		}
		diags.Append(v.As(ctx, &q, basetypes.ObjectAsOptions{})...)
		return &console.ResourceSpec{Cpu: stringPtr(q.CPU), Memory: stringPtr(q.Memory)}
	}
	out := &console.ResourceRequirementsSpec{Requests: quantity("requests"), Limits: quantity("limits")}
	return out, diags
}

func rabbitMQResourcesToObject(spec *console.ResourceRequirementsSpec) (types.Object, diag.Diagnostics) {
	if spec == nil {
		return types.ObjectNull(rabbitMQResourcesAttrTypes), nil
	}
	var diags diag.Diagnostics
	quantity := func(q *console.ResourceSpec) types.Object {
		if q == nil {
			return types.ObjectNull(rabbitMQQuantityAttrTypes)
		}
		v, d := types.ObjectValue(rabbitMQQuantityAttrTypes, map[string]attr.Value{
			"cpu":    optString(q.Cpu),
			"memory": optString(q.Memory),
		})
		diags.Append(d...)
		return v
	}
	v, d := types.ObjectValue(rabbitMQResourcesAttrTypes, map[string]attr.Value{
		"requests": quantity(spec.Requests),
		"limits":   quantity(spec.Limits),
	})
	diags.Append(d...)
	return v, diags
}

// parseRabbitMQStorageBytes parses the whole Kubernetes quantities the CRD
// accepts (`20Gi`, `500M`, `1073741824`) into bytes, as the platform does to
// refuse a shrink. ok is false for anything else.
func parseRabbitMQStorageBytes(s string) (*big.Int, bool) {
	if !rabbitMQStorageRe.MatchString(s) {
		return nil, false
	}
	end := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(s)
	}
	n, ok := new(big.Int).SetString(s[:end], 10)
	if !ok {
		return nil, false
	}
	var mult *big.Int
	switch s[end:] {
	case "":
		mult = big.NewInt(1)
	case "Ki", "Mi", "Gi", "Ti", "Pi":
		exp := uint(10 * (1 + strings.Index("KMGTP", s[end:end+1])))
		mult = new(big.Int).Lsh(big.NewInt(1), exp)
	case "k", "M", "G", "T", "P":
		exp := int64(3 * (1 + strings.Index("kMGTP", s[end:])))
		mult = new(big.Int).Exp(big.NewInt(10), big.NewInt(exp), nil)
	default:
		return nil, false
	}
	return n.Mul(n, mult), true
}

// rabbitMQReplicasNoDecrease refuses at plan time to lower `replicas`: the
// platform rejects a scale-down, so letting it through would only fail apply.
type rabbitMQReplicasNoDecrease struct{}

func (rabbitMQReplicasNoDecrease) Description(context.Context) string {
	return "Refuses to lower the node count of an existing broker."
}

func (m rabbitMQReplicasNoDecrease) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (rabbitMQReplicasNoDecrease) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || resp.PlanValue.IsNull() || resp.PlanValue.IsUnknown() {
		return
	}
	prev, next := req.StateValue.ValueInt64(), resp.PlanValue.ValueInt64()
	if next < prev {
		resp.Diagnostics.AddAttributeError(req.Path, "RabbitMQ replicas cannot decrease",
			fmt.Sprintf("replicas cannot go from %d to %d: a RabbitMQ cluster cannot be scaled down. "+
				"Keep %d or more, or replace the broker.", prev, next, prev))
	}
}

// rabbitMQStorageNoShrink refuses at plan time to shrink `storage`, comparing
// sizes across units (`2Ti` grows `20Gi`, `21G` shrinks it).
type rabbitMQStorageNoShrink struct{}

func (rabbitMQStorageNoShrink) Description(context.Context) string {
	return "Refuses to shrink the volume of an existing broker."
}

func (m rabbitMQStorageNoShrink) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (rabbitMQStorageNoShrink) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || resp.PlanValue.IsNull() || resp.PlanValue.IsUnknown() {
		return
	}
	prev, next := req.StateValue.ValueString(), resp.PlanValue.ValueString()
	prevBytes, ok1 := parseRabbitMQStorageBytes(prev)
	nextBytes, ok2 := parseRabbitMQStorageBytes(next)
	if !ok1 || !ok2 {
		// An invalid value is the validator's to report.
		return
	}
	if nextBytes.Cmp(prevBytes) < 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "RabbitMQ storage cannot shrink",
			fmt.Sprintf("storage cannot go from %s to %s: volumes can only grow. Keep %s or more, or replace the broker.",
				prev, next, prev))
	}
}

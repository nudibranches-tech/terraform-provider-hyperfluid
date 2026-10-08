// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// container_app_resource.go — scalar core of the CaaS resource. The nested
// blocks (env, secret_refs, image_pull_secrets, file_mounts, persistence,
// custom_domains) are deliberately deferred to a follow-up PR. This PR
// establishes the two hard patterns: resource_version optimistic concurrency
// (H4) and the resource_tier→cpu/mem read-mapping (M2: the /crd GET returns
// cpu/mem, never resource_tier, so resource_tier is preserved from prior state).

var (
	_ resource.Resource                   = &containerAppResource{}
	_ resource.ResourceWithValidateConfig = &containerAppResource{}
	_ resource.ResourceWithConfigure      = &containerAppResource{}
	_ resource.ResourceWithImportState    = &containerAppResource{}
)

// maxPortEntries matches the CRD's cap.
const maxPortEntries = 16

const containerAppWaitTimeout = 5 * time.Minute

func NewContainerAppResource() resource.Resource {
	return &containerAppResource{}
}

type containerAppResource struct {
	p *providerData
}

type containerAppModel struct {
	ID               types.String `tfsdk:"id"`
	Env              types.String `tfsdk:"env"`
	Name             types.String `tfsdk:"name"`
	ImageRepository  types.String `tfsdk:"image_repository"`
	ImageTag         types.String `tfsdk:"image_tag"`
	Port             types.Int64  `tfsdk:"port"`
	Replicas         types.Int64  `tfsdk:"replicas"`
	Enabled          types.Bool   `tfsdk:"enabled"`
	ExposeToInternet types.Bool   `tfsdk:"expose_to_internet"`
	ResourceTier     types.String `tfsdk:"resource_tier"`
	HealthCheckPath  types.String `tfsdk:"health_check_path"`
	HealthCheckPort  types.Int64  `tfsdk:"health_check_port"`

	// computed
	ResourceVersion   types.String `tfsdk:"resource_version"`
	CPURequest        types.String `tfsdk:"cpu_request"`
	CPULimit          types.String `tfsdk:"cpu_limit"`
	MemoryRequest     types.String `tfsdk:"memory_request"`
	MemoryLimit       types.String `tfsdk:"memory_limit"`
	Phase             types.String `tfsdk:"phase"`
	Endpoint          types.String `tfsdk:"endpoint"`
	DesiredReplicas   types.Int64  `tfsdk:"desired_replicas"`
	AvailableReplicas types.Int64  `tfsdk:"available_replicas"`
	Slug              types.String `tfsdk:"slug"`

	// A framework type, not a Go slice: `ports` is Optional+Computed, so it is
	// unknown in the plan whenever the config leaves it out, and only these types
	// can carry an unknown value.
	Ports types.List `tfsdk:"ports"`

	// ImageSource is the `image_source` block, null when the image is literal.
	ImageSource   types.Object `tfsdk:"image_source"`
	ResolvedImage types.String `tfsdk:"resolved_image"`
	ResolvedRef   types.String `tfsdk:"resolved_ref"`
	Revision      types.String `tfsdk:"revision"`
	LastSyncedAt  types.String `tfsdk:"last_synced_at"`
	SyncError     types.String `tfsdk:"sync_error"`
}

// containerAppPortModel is one published port.
type containerAppPortModel struct {
	Name     types.String `tfsdk:"name"`
	Port     types.Int64  `tfsdk:"port"`
	Protocol types.String `tfsdk:"protocol"`
	Primary  types.Bool   `tfsdk:"primary"`
}

func containerAppPortType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":     types.StringType,
		"port":     types.Int64Type,
		"protocol": types.StringType,
		"primary":  types.BoolType,
	}}
}

// portProtocols are the platform's port kinds, as the console offers them: one
// choice that carries both the L4 protocol and, for HTTP, the L7 one the
// ingress needs. Only HTTP is publishable — raw TCP/UDP is reachable in-cluster
// through the app's Service.
var portProtocols = []string{"HTTP", "TCP", "UDP"}

// wireProtocols maps a port kind onto the (L4, L7) pair the API takes.
var wireProtocols = map[string]struct {
	protocol    console.PortProtocol
	appProtocol *console.AppProtocol
}{
	"HTTP": {protocol: console.PortProtocolTCP, appProtocol: ptr(console.AppProtocolHttp)},
	"TCP":  {protocol: console.PortProtocolTCP},
	"UDP":  {protocol: console.PortProtocolUDP},
}

func ptr[T any](v T) *T { return &v }

// portProtocolOf collapses the API's pair back into the single kind.
func portProtocolOf(protocol console.PortProtocol, appProtocol *console.AppProtocol) string {
	if appProtocol != nil && *appProtocol == console.AppProtocolHttp {
		return "HTTP"
	}
	if protocol == console.PortProtocolUDP {
		return "UDP"
	}
	return "TCP"
}

func (r *containerAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_app"
}

func (r *containerAppResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedString := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A container app (CaaS). Scalar core; nested env/secret/mount blocks land in a follow-up.\n\n" +
			"## Deploying from Git\n\n" +
			"An `image_source { git { ... } }` block replaces the literal `image_repository` / `image_tag`: the platform " +
			"polls a `hyperfluid.toml` file in the repository and rolls the app out when this app's entry changes. It " +
			"keeps running the last good image when a check fails, and a failing check raises an alert unless " +
			"`alert_on_sync_failure` is `false`. The repository is read with an organization-wide `hyperfluid_secret` " +
			"of type `scm_credential`, named in `credential`.\n\n" +
			"Every optional field of the block stays unset when omitted, and the platform resolves its default itself " +
			"(the default branch, `hyperfluid.toml`, an entry named after the app, a check every 5 minutes). The image " +
			"that runs is never written to `image_repository` / `image_tag`: read it from `resolved_image`, " +
			"`resolved_ref`, `revision` and `last_synced_at`, which Terraform never plans a change for, so a release " +
			"made through Git is not drift. Terraform waits for the first check of a new source, not for the rollouts " +
			"that follow it; a check that fails or is slow is a warning, with the reason in `sync_error`.\n\n" +
			"Removing the block detaches the source: the app keeps running the image it last resolved until the " +
			"literal `image_repository` / `image_tag` you set in its place is applied.\n\n" +
			"~> Push access to the tracked branch, or to a tag matching `tag_pattern`, is deploy access. Protect them " +
			"(GitHub rulesets, GitLab protected branches and tags, Forgejo protected branches and tags).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id the app runs in. Changing this forces a new app.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "App name (slug). Changing this forces a new app.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"image_repository": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Container image repository. Required unless `image_source` is set, and conflicts with it.",
			},
			"image_tag": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Container image tag. Required unless `image_source` is set, and conflicts with it.",
			},
			"port": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "The app's single container port. **Deprecated — use `ports`**, which " +
					"this conflicts with.\n\n" +
					"~> The platform ignores writes to this attribute once the app's spec carries a " +
					"non-empty `ports`, so on such an app a change here applies cleanly in Terraform and " +
					"does nothing. Move the app to `ports` rather than editing this.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"replicas": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Desired replica count.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Whether the app is running. Defaults to true.",
				Default:             booldefault.StaticBool(true),
			},
			"expose_to_internet": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Whether internet-facing routes (platform host route and custom-domain routes) are created for the app. Defaults to false (reachable only in-cluster), matching the platform's private-by-default posture. Set true to publish internet-facing routes.",
				Default:             booldefault.StaticBool(false),
			},
			"resource_tier": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Resource tier: nano, micro, small, medium, large, xlarge. Maps to cpu/memory server-side.",
				Validators: []validator.String{
					stringvalidator.OneOf("nano", "micro", "small", "medium", "large", "xlarge"),
				},
			},
			"health_check_path": schema.StringAttribute{Optional: true, MarkdownDescription: "HTTP health check path."},
			"health_check_port": schema.Int64Attribute{Optional: true, MarkdownDescription: "HTTP health check port."},

			"resource_version":   computedString("Kubernetes resourceVersion; used for optimistic concurrency on update."),
			"cpu_request":        computedString("CPU request derived from resource_tier."),
			"cpu_limit":          computedString("CPU limit derived from resource_tier."),
			"memory_request":     computedString("Memory request derived from resource_tier."),
			"memory_limit":       computedString("Memory limit derived from resource_tier."),
			"phase":              computedString("Current lifecycle phase."),
			"endpoint":           computedString("Public endpoint, once provisioned."),
			"desired_replicas":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Desired replicas reported by the platform."},
			"available_replicas": schema.Int64Attribute{Computed: true, MarkdownDescription: "Available replicas reported by the platform."},
			"slug":               computedString("Derived slug. This is the name a `hyperfluid_service_link` endpoint takes — `name` is a display name and the two differ as soon as it contains anything a slug cannot."),
			"ports": schema.ListNestedAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "The ports the container listens on, replacing the deprecated single " +
					"`port`. Only the port marked `primary` gets a public route; the rest are reachable " +
					"in-cluster through the app's Service. Leave it out and the platform derives a single " +
					"entry from `port`.\n\n" +
					"Assignable straight to a `hyperfluid_service_link`'s `target_ports`, which reads the " +
					"`port` and `protocol` of each entry.",
				Validators: []validator.List{
					listvalidator.ConflictsWith(path.MatchRoot("port")),
					listvalidator.SizeAtMost(maxPortEntries),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Port name, unique within the app, e.g. `http` or `metrics`. " +
								"Lowercase letters, digits and hyphens, with at least one letter and no leading, " +
								"trailing or consecutive hyphens; 15 characters at most.",
							Validators: []validator.String{
								stringvalidator.LengthBetween(1, 15),
								// Kubernetes validates this as an IANA_SVC_NAME, stricter than a
								// DNS-1123 label. Split in two because RE2 has no lookahead.
								stringvalidator.RegexMatches(
									regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`),
									"must be lowercase letters, digits and hyphens, with no leading, trailing or consecutive hyphens",
								),
								stringvalidator.RegexMatches(
									regexp.MustCompile(`[a-z]`),
									"must contain at least one letter",
								),
							},
						},
						"port": schema.Int64Attribute{
							Required:            true,
							MarkdownDescription: "Port the container listens on.",
							Validators:          []validator.Int64{int64validator.Between(1, 65535)},
						},
						"protocol": schema.StringAttribute{
							Optional: true, Computed: true,
							Default: stringdefault.StaticString("HTTP"),
							MarkdownDescription: "One of `HTTP`, `TCP` or `UDP`, defaulting to `HTTP`. Only an " +
								"`HTTP` port can be published: the platform ingress terminates HTTP(S) and has no " +
								"listener for anything else, so a `TCP` or `UDP` port is reachable in-cluster " +
								"through the app's Service only.",
							Validators: []validator.String{stringvalidator.OneOf(portProtocols...)},
						},
						"primary": schema.BoolAttribute{
							Optional: true, Computed: true,
							MarkdownDescription: "Marks the port that public routes and the default health probe " +
								"target. Only an `HTTP` port can be primary, and at most one port may be. A single " +
								"`HTTP` port becomes primary on its own.",
						},
					},
				},
			},
		},
		Blocks: map[string]schema.Block{
			"image_source": imageSourceBlock(),
		},
	}
	for name, a := range gitStatusAttributes() {
		resp.Schema.Attributes[name] = a
	}
}

func (r *containerAppResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// validateImage requires a literal image unless a Git source supplies it. The
// two together are refused by the schema (image_source conflicts with both
// image attributes); the half-set literal image is what it cannot express.
func validateImage(cfg containerAppModel, diags *diag.Diagnostics) {
	if !cfg.ImageSource.IsNull() {
		// A nested block cannot be marked required: the framework would demand it
		// of an absent parent too.
		git, ok := cfg.ImageSource.Attributes()["git"].(types.Object)
		if !ok || git.IsNull() {
			diags.AddAttributeError(path.Root("image_source").AtName("git"), "Missing git block",
				"image_source needs a git block naming the repository that declares the image.")
			return
		}
		for _, name := range []string{"provider", "repository"} {
			if v, ok := git.Attributes()[name].(types.String); ok && v.IsNull() {
				diags.AddAttributeError(path.Root("image_source").AtName("git").AtName(name), "Missing "+name,
					"The git block requires "+name+".")
			}
		}
		return
	}
	if cfg.ImageRepository.IsNull() {
		diags.AddAttributeError(path.Root("image_repository"), "Missing image",
			"image_repository is required unless an image_source block supplies the image.")
	}
	if cfg.ImageTag.IsNull() {
		diags.AddAttributeError(path.Root("image_tag"), "Missing image",
			"image_tag is required unless an image_source block supplies the image.")
	}
}

// ValidateConfig applies the platform's port rules at plan time. The API checks
// most of them too, but the exposure rule it does not: it accepts
// expose_to_internet on an app with no HTTP port and simply wires no routes, so
// the app ends up unreachable with nothing to say why.
func (r *containerAppResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg containerAppModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateImage(cfg, &resp.Diagnostics)
	if cfg.Ports.IsNull() || cfg.Ports.IsUnknown() {
		return
	}
	var ports []containerAppPortModel
	resp.Diagnostics.Append(cfg.Ports.ElementsAs(ctx, &ports, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	httpPorts, primaries := 0, 0
	for i, p := range ports {
		isHTTP := p.Protocol.ValueString() == "HTTP" || p.Protocol.IsNull()
		if isHTTP {
			httpPorts++
		}
		if p.Primary.ValueBool() {
			primaries++
			if !isHTTP {
				resp.Diagnostics.AddAttributeError(
					path.Root("ports").AtListIndex(i).AtName("primary"),
					"Only an HTTP port can be primary",
					"Raw TCP and UDP cannot be published: the platform ingress terminates HTTP(S) and has no "+
						"listener for anything else. Set protocol to HTTP, or drop primary.",
				)
			}
		}
	}

	if primaries > 1 {
		resp.Diagnostics.AddAttributeError(path.Root("ports"), "At most one port may be primary",
			fmt.Sprintf("%d ports are marked primary. Exactly one carries the public routes and the default health probe.", primaries))
	}
	if len(ports) > 1 && httpPorts > 0 && primaries == 0 {
		resp.Diagnostics.AddAttributeError(path.Root("ports"), "One port must be primary",
			"An app declaring several ports must mark exactly one HTTP port primary, so the platform knows which one its routes and health probe target.")
	}
	if cfg.ExposeToInternet.ValueBool() && httpPorts == 0 {
		resp.Diagnostics.AddAttributeError(path.Root("expose_to_internet"), "Nothing to expose",
			"The app publishes no HTTP port, so there is nothing the platform ingress can route to and the app would stay unreachable. "+
				"Give a port protocol HTTP, or leave expose_to_internet off — raw TCP and UDP ports are reachable in-cluster through the app's Service.")
	}
}

func (r *containerAppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg containerAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ports, d := portInputs(ctx, cfg.Ports)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	src, d := gitSourceFromObject(ctx, plan.ImageSource)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := plan.Env.ValueString()
	body := console.CreateContainerAppCrdRequestBody{
		Name:             plan.Name.ValueString(),
		ImageRepository:  stringPtr(plan.ImageRepository),
		ImageTag:         stringPtr(plan.ImageTag),
		Enabled:          enabledOrDefault(plan.Enabled),
		ExposeToInternet: boolPtr(plan.ExposeToInternet),
		Port:             int32PtrFromInt64(plan.Port),
		Replicas:         int32PtrFromInt64(plan.Replicas),
		HealthCheckPath:  stringPtr(plan.HealthCheckPath),
		HealthCheckPort:  int32PtrFromInt64(plan.HealthCheckPort),
	}
	if !plan.ResourceTier.IsNull() {
		tier := console.ResourceTier(plan.ResourceTier.ValueString())
		body.ResourceTier = &tier
	}
	// `ports` replaces `port`, so only one of the two is ever sent.
	if ports != nil {
		body.Ports = &ports
		body.Port = nil
	}
	if src != nil {
		is, err := buildImageSource(*src)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("image_source"), "Invalid image source", err.Error())
			return
		}
		body.ImageSource = &is
		body.AlertOnSyncFailure = boolPtr(src.AlertOnSyncFailure)
	}

	if err := r.p.API.CreateContainerApp(ctx, r.p.OrgID, env, body); err != nil {
		resp.Diagnostics.AddError("Failed to create container app", err.Error())
		return
	}

	appID, err := r.p.API.FindContainerAppID(ctx, r.p.OrgID, env, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve created container app id", err.Error())
		return
	}

	if err := r.waitDeployed(ctx, appID, src != nil, &resp.Diagnostics); err != nil {
		resp.Diagnostics.AddError("Container app did not become ready", err.Error())
		return
	}

	state, err := r.readState(ctx, appID, plan.ResourceTier, plan.ImageSource)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read container app after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *containerAppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior containerAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, err := r.readState(ctx, prior.ID.ValueString(), prior.ResourceTier, prior.ImageSource)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read container app", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *containerAppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg containerAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The config, not the plan: `ports` is Optional+Computed, so the plan carries
	// the prior value when the config omits it, and PATCH distinguishes the two —
	// an omitted `ports` leaves the existing entries alone, an empty one clears
	// them, a non-empty one replaces the list.
	ports, d := portInputs(ctx, cfg.Ports)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	appID := state.ID.ValueString()
	resourceVersion := state.ResourceVersion
	body := console.PatchContainerAppCrdRequestBody{
		Enabled:          boolPtr(plan.Enabled),
		ExposeToInternet: boolPtr(plan.ExposeToInternet),
		Port:             int32PtrFromInt64(plan.Port),
		Replicas:         int32PtrFromInt64(plan.Replicas),
		HealthCheckPath:  stringPtr(plan.HealthCheckPath),
		HealthCheckPort:  int32PtrFromInt64(plan.HealthCheckPort),
	}
	if !plan.ResourceTier.IsNull() {
		tier := console.ResourceTier(plan.ResourceTier.ValueString())
		body.ResourceTier = &tier
	}
	if ports != nil {
		body.Ports = &ports
		body.Port = nil
	}

	srcPlan, d := gitSourceFromObject(ctx, plan.ImageSource)
	resp.Diagnostics.Append(d...)
	srcState, d := gitSourceFromObject(ctx, state.ImageSource)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	switch {
	case srcPlan != nil:
		// The platform refuses image fields while a source is attached, and an
		// unchanged source is left alone so an edit elsewhere never re-registers it.
		if !plan.ImageSource.Equal(state.ImageSource) {
			is, err := buildImageSource(*srcPlan)
			if err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("image_source"), "Invalid image source", err.Error())
				return
			}
			body.ImageSource = &is
			body.AlertOnSyncFailure = boolPtr(srcPlan.AlertOnSyncFailure)
			if srcPlan.AlertOnSyncFailure.IsNull() && srcState != nil && !srcState.AlertOnSyncFailure.IsNull() {
				// Dropping the setting hands the choice back to the platform, which
				// raises the alert.
				body.AlertOnSyncFailure = ptr(true)
			}
		}
	case srcState != nil:
		// Detaching pins the image the source last resolved; the literal image
		// is then applied on top of it. Detaching bumps the spec generation.
		if err := r.p.API.DetachImageSource(ctx, r.p.OrgID, appID); err != nil {
			resp.Diagnostics.AddError("Failed to detach the Git image source", err.Error())
			return
		}
		detached, err := r.p.API.GetContainerAppSpec(ctx, r.p.OrgID, appID)
		if err != nil {
			resp.Diagnostics.AddError("Failed to read container app after detaching its Git source", err.Error())
			return
		}
		resourceVersion = types.StringValue(detached.ResourceVersion)
		body.ImageRepository = stringPtr(plan.ImageRepository)
		body.ImageTag = stringPtr(plan.ImageTag)
	default:
		body.ImageRepository = stringPtr(plan.ImageRepository)
		body.ImageTag = stringPtr(plan.ImageTag)
	}
	body.ResourceVersion = stringPtr(resourceVersion) // H4 optimistic concurrency

	if err := r.p.API.PatchContainerApp(ctx, r.p.OrgID, appID, body); err != nil {
		resp.Diagnostics.AddError("Failed to update container app", err.Error())
		return
	}
	if err := r.waitDeployed(ctx, appID, srcPlan != nil, &resp.Diagnostics); err != nil {
		resp.Diagnostics.AddError("Container app did not become ready after update", err.Error())
		return
	}

	newState, err := r.readState(ctx, appID, plan.ResourceTier, plan.ImageSource)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read container app after update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *containerAppResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state containerAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	appID := state.ID.ValueString()
	if err := r.p.API.DeleteContainerApp(ctx, r.p.OrgID, appID); err != nil {
		resp.Diagnostics.AddError("Failed to delete container app", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, containerAppWaitTimeout, func() error {
		_, err := r.p.API.GetContainerAppStatus(ctx, r.p.OrgID, appID)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Container app still present after delete", err.Error())
	}
}

func (r *containerAppResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// waitReady blocks until the rollout has converged to the reconciled spec's
// desired replica count. It reads desired from the spec (not the status) so it
// doesn't return prematurely on the transient desired=0 the status reports
// before the operator scales the workload up.
func (r *containerAppResource) waitReady(ctx context.Context, appID string) error {
	_, err := waitForReady(ctx, containerAppWaitTimeout, func() (*console.ContainerAppResponse, bool, error) {
		spec, err := r.p.API.GetContainerAppSpec(ctx, r.p.OrgID, appID)
		if err != nil {
			return nil, false, err
		}
		st, err := r.p.API.GetContainerAppStatus(ctx, r.p.OrgID, appID)
		if err != nil {
			return nil, false, err
		}
		var want int32
		if spec.Enabled {
			want = spec.Replicas
		}
		ready := st.DesiredReplicas == want &&
			st.AvailableReplicas == want &&
			st.UpdatedReadyReplicas == want
		return st, ready, nil
	})
	return err
}

// waitDeployed waits for the app to converge. A Git-sourced app has no image
// until the platform's first check resolves one, so that comes first; a check
// that fails or is slow is reported as a warning, not an error, because the
// platform keeps retrying it and the app is created either way.
func (r *containerAppResource) waitDeployed(ctx context.Context, appID string, gitSourced bool, diags *diag.Diagnostics) error {
	if !gitSourced {
		return r.waitReady(ctx, appID)
	}
	spec, err := waitForReady(ctx, containerAppWaitTimeout, func() (*console.ContainerAppCrdSpecResponse, bool, error) {
		spec, err := r.p.API.GetContainerAppSpec(ctx, r.p.OrgID, appID)
		if err != nil {
			return nil, false, err
		}
		st := spec.ImageSourceStatus
		return spec, st != nil && (st.Resolved != nil || st.Error != nil), nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		diags.AddWarning("Git image source not synced yet",
			"The platform has not finished its first check of the repository ("+err.Error()+"). The app is "+
				"created and will deploy once a check succeeds; see `sync_error` and `resolved_image`.")
		return nil
	}
	if st := spec.ImageSourceStatus; st.Error != nil {
		diags.AddWarning("Git image source check failed",
			"The platform could not resolve an image from the repository: "+*st.Error+". The app keeps running "+
				"its last good image, if it has one, and the check is retried.")
		return nil
	}
	return r.waitReady(ctx, appID)
}

// readState is readInto for the resource: the image the platform resolved from
// a Git source lives in the computed attributes, so image_repository and
// image_tag stay null and never disagree with a config that leaves them out.
func (r *containerAppResource) readState(ctx context.Context, appID string, priorTier types.String, priorSource types.Object) (containerAppModel, error) {
	m, err := r.readInto(ctx, appID, priorTier, priorSource)
	if err != nil {
		return m, err
	}
	if !m.ImageSource.IsNull() {
		m.ImageRepository = types.StringNull()
		m.ImageTag = types.StringNull()
	}
	return m, nil
}

func containerAppPorts(ctx context.Context, ports []console.ContainerAppPortResponse) (types.List, diag.Diagnostics) {
	out := make([]containerAppPortModel, 0, len(ports))
	for _, p := range ports {
		out = append(out, containerAppPortModel{
			Name:     optString(p.Name),
			Port:     types.Int64Value(int64(p.ContainerPort)),
			Protocol: types.StringValue(portProtocolOf(p.Protocol, p.AppProtocol)),
			Primary:  types.BoolValue(p.Primary),
		})
	}
	return types.ListValueFrom(ctx, containerAppPortType(), out)
}

// portInputs converts a configured `ports` into the API input. Returns nil when
// the config leaves it out, which the caller sends as an absent field.
func portInputs(ctx context.Context, list types.List) ([]console.ContainerAppPortInput, diag.Diagnostics) {
	if list.IsNull() || list.IsUnknown() {
		return nil, nil
	}
	var models []containerAppPortModel
	d := list.ElementsAs(ctx, &models, false)
	if d.HasError() {
		return nil, d
	}
	out := make([]console.ContainerAppPortInput, 0, len(models))
	for _, m := range models {
		in := console.ContainerAppPortInput{
			Name:          m.Name.ValueString(),
			ContainerPort: int32(m.Port.ValueInt64()),
		}
		if wire, ok := wireProtocols[m.Protocol.ValueString()]; ok {
			in.Protocol = &wire.protocol
			in.AppProtocol = wire.appProtocol
		}
		if !m.Primary.IsNull() && !m.Primary.IsUnknown() {
			in.Primary = m.Primary.ValueBoolPointer()
		}
		out = append(out, in)
	}
	return out, d
}

// primaryPort reads the single port this schema exposes. The API's `port` is
// deprecated in favour of `ports`, and is null for an app whose ports were set
// through the newer field, so fall back to the entry flagged primary.
func primaryPort(spec *console.ContainerAppCrdSpecResponse) types.Int64 {
	if spec.Port != nil {
		return types.Int64Value(int64(*spec.Port))
	}
	for _, p := range spec.Ports {
		if p.Primary {
			return types.Int64Value(int64(p.ContainerPort))
		}
	}
	return types.Int64Null()
}

// readInto builds the model from both views. priorTier is carried through
// because the /crd spec response does not echo resource_tier (M2).
func (r *containerAppResource) readInto(ctx context.Context, appID string, priorTier types.String, priorSource types.Object) (containerAppModel, error) {
	spec, err := r.p.API.GetContainerAppSpec(ctx, r.p.OrgID, appID)
	if err != nil {
		return containerAppModel{}, err
	}
	status, err := r.p.API.GetContainerAppStatus(ctx, r.p.OrgID, appID)
	if err != nil {
		return containerAppModel{}, err
	}
	ports, d := containerAppPorts(ctx, spec.Ports)
	if d.HasError() {
		return containerAppModel{}, errors.New("failed to convert container app ports")
	}

	prior, d := gitSourceFromObject(ctx, priorSource)
	if d.HasError() {
		return containerAppModel{}, errors.New("failed to read the prior image_source block")
	}
	imageSource, err := gitSourceToObject(spec.ImageSource, spec.AlertOnSyncFailure, prior)
	if err != nil {
		return containerAppModel{}, err
	}
	gs := nullGitStatus()
	if spec.ImageSource != nil {
		gs = gitStatusFrom(spec.ImageSourceStatus)
	}

	m := containerAppModel{
		ID:                types.StringValue(appID),
		Env:               types.StringValue(status.HarborId.String()),
		Name:              types.StringValue(status.Name),
		ImageRepository:   optNonEmpty(&spec.ImageRepository),
		ImageTag:          optNonEmpty(&spec.ImageTag),
		Port:              primaryPort(spec),
		Replicas:          types.Int64Value(int64(spec.Replicas)),
		Enabled:           types.BoolValue(spec.Enabled),
		ExposeToInternet:  types.BoolValue(spec.ExposeToInternet),
		ResourceTier:      priorTier, // preserved; not returned by the API
		HealthCheckPath:   optString(spec.HealthCheckPath),
		HealthCheckPort:   optInt64FromInt32(spec.HealthCheckPort),
		ResourceVersion:   types.StringValue(spec.ResourceVersion),
		CPURequest:        optString(spec.CpuRequest),
		CPULimit:          optString(spec.CpuLimit),
		MemoryRequest:     optString(spec.MemoryRequest),
		MemoryLimit:       optString(spec.MemoryLimit),
		Phase:             optString(status.Phase),
		Endpoint:          optString(status.Endpoint),
		DesiredReplicas:   types.Int64Value(int64(status.DesiredReplicas)),
		AvailableReplicas: types.Int64Value(int64(status.AvailableReplicas)),
		Slug:              types.StringValue(status.Slug),
		Ports:             ports,
		ImageSource:       imageSource,
		ResolvedImage:     gs.ResolvedImage,
		ResolvedRef:       gs.ResolvedRef,
		Revision:          gs.Revision,
		LastSyncedAt:      gs.LastSyncedAt,
		SyncError:         gs.SyncError,
	}
	return m, nil
}

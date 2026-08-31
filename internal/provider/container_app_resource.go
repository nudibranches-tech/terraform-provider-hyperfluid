// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

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
	_ resource.Resource                = &containerAppResource{}
	_ resource.ResourceWithConfigure   = &containerAppResource{}
	_ resource.ResourceWithImportState = &containerAppResource{}
)

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
}

// containerAppPortModel is one published port.
type containerAppPortModel struct {
	Name        types.String `tfsdk:"name"`
	Port        types.Int64  `tfsdk:"port"`
	Protocol    types.String `tfsdk:"protocol"`
	Primary     types.Bool   `tfsdk:"primary"`
	AppProtocol types.String `tfsdk:"app_protocol"`
}

func containerAppPortType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":         types.StringType,
		"port":         types.Int64Type,
		"protocol":     types.StringType,
		"primary":      types.BoolType,
		"app_protocol": types.StringType,
	}}
}

func (r *containerAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_app"
}

func (r *containerAppResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedString := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A container app (CaaS). Scalar core; nested env/secret/mount blocks land in a follow-up.",
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
			"image_repository": schema.StringAttribute{Required: true, MarkdownDescription: "Container image repository."},
			"image_tag":        schema.StringAttribute{Required: true, MarkdownDescription: "Container image tag."},
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
				Validators: []validator.List{listvalidator.ConflictsWith(path.MatchRoot("port"))},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Port name, unique within the app, e.g. `http` or `metrics`. " +
								"A DNS-1123 label of at most 15 characters.",
						},
						"port": schema.Int64Attribute{Required: true, MarkdownDescription: "Port the container listens on."},
						"protocol": schema.StringAttribute{
							Optional: true, Computed: true,
							MarkdownDescription: "L4 protocol: `TCP`, `UDP` or `SCTP`. Defaults to `TCP`.",
							Validators:          []validator.String{stringvalidator.OneOf("TCP", "UDP", "SCTP")},
						},
						"primary": schema.BoolAttribute{
							Optional: true, Computed: true,
							MarkdownDescription: "Marks the port public routes and the default health probe " +
								"target. Optional when the app declares a single port with an `app_protocol`.",
						},
						"app_protocol": schema.StringAttribute{
							Optional: true, Computed: true,
							MarkdownDescription: "L7 protocol, for a port the platform ingress should route. " +
								"`http` is the only value the ingress has a listener for today.",
							Validators: []validator.String{stringvalidator.OneOf(string(console.Http))},
						},
					},
				},
			},
		},
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

	env := plan.Env.ValueString()
	body := console.CreateContainerAppCrdRequestBody{
		Name:             plan.Name.ValueString(),
		ImageRepository:  plan.ImageRepository.ValueString(),
		ImageTag:         plan.ImageTag.ValueString(),
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

	if err := r.p.API.CreateContainerApp(ctx, r.p.OrgID, env, body); err != nil {
		resp.Diagnostics.AddError("Failed to create container app", err.Error())
		return
	}

	appID, err := r.p.API.FindContainerAppID(ctx, r.p.OrgID, env, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve created container app id", err.Error())
		return
	}

	if err := r.waitReady(ctx, appID); err != nil {
		resp.Diagnostics.AddError("Container app did not become ready", err.Error())
		return
	}

	state, err := r.readInto(ctx, env, appID, plan.ResourceTier)
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

	state, err := r.readInto(ctx, prior.Env.ValueString(), prior.ID.ValueString(), prior.ResourceTier)
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
	body := console.PatchContainerAppCrdRequestBody{
		ImageRepository:  stringPtr(plan.ImageRepository),
		ImageTag:         stringPtr(plan.ImageTag),
		Enabled:          boolPtr(plan.Enabled),
		ExposeToInternet: boolPtr(plan.ExposeToInternet),
		Port:             int32PtrFromInt64(plan.Port),
		Replicas:         int32PtrFromInt64(plan.Replicas),
		HealthCheckPath:  stringPtr(plan.HealthCheckPath),
		HealthCheckPort:  int32PtrFromInt64(plan.HealthCheckPort),
		ResourceVersion:  stringPtr(state.ResourceVersion), // H4 optimistic concurrency
	}
	if !plan.ResourceTier.IsNull() {
		tier := console.ResourceTier(plan.ResourceTier.ValueString())
		body.ResourceTier = &tier
	}
	if ports != nil {
		body.Ports = &ports
		body.Port = nil
	}

	if err := r.p.API.PatchContainerApp(ctx, r.p.OrgID, appID, body); err != nil {
		resp.Diagnostics.AddError("Failed to update container app", err.Error())
		return
	}
	if err := r.waitReady(ctx, appID); err != nil {
		resp.Diagnostics.AddError("Container app did not become ready after update", err.Error())
		return
	}

	newState, err := r.readInto(ctx, plan.Env.ValueString(), appID, plan.ResourceTier)
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

func containerAppPorts(ctx context.Context, ports []console.ContainerAppPortResponse) (types.List, diag.Diagnostics) {
	out := make([]containerAppPortModel, 0, len(ports))
	for _, p := range ports {
		appProtocol := types.StringNull()
		if p.AppProtocol != nil {
			appProtocol = types.StringValue(string(*p.AppProtocol))
		}
		out = append(out, containerAppPortModel{
			Name:        optString(p.Name),
			Port:        types.Int64Value(int64(p.ContainerPort)),
			Protocol:    types.StringValue(string(p.Protocol)),
			Primary:     types.BoolValue(p.Primary),
			AppProtocol: appProtocol,
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
		if !m.Protocol.IsNull() && !m.Protocol.IsUnknown() {
			proto := console.PortProtocol(m.Protocol.ValueString())
			in.Protocol = &proto
		}
		if !m.Primary.IsNull() && !m.Primary.IsUnknown() {
			in.Primary = m.Primary.ValueBoolPointer()
		}
		if !m.AppProtocol.IsNull() && !m.AppProtocol.IsUnknown() {
			ap := console.AppProtocol(m.AppProtocol.ValueString())
			in.AppProtocol = &ap
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
func (r *containerAppResource) readInto(ctx context.Context, env, appID string, priorTier types.String) (containerAppModel, error) {
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

	m := containerAppModel{
		ID:                types.StringValue(appID),
		Env:               types.StringValue(env),
		Name:              types.StringValue(status.Name),
		ImageRepository:   types.StringValue(spec.ImageRepository),
		ImageTag:          types.StringValue(spec.ImageTag),
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
	}
	return m, nil
}

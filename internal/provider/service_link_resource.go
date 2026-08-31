// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// service_link_resource.go — a declared "this service may reach that one" edge
// (ADR-0021). Workload isolation is strict, so a link is what makes one service
// reachable from another; anything undeclared stays denied.
//
// The resource is create/read/delete only. There is no PATCH endpoint and the
// CRD holds every field immutable, so each attribute forces replacement.

var (
	_ resource.Resource                   = &serviceLinkResource{}
	_ resource.ResourceWithConfigure      = &serviceLinkResource{}
	_ resource.ResourceWithImportState    = &serviceLinkResource{}
	_ resource.ResourceWithValidateConfig = &serviceLinkResource{}
)

const serviceLinkWaitTimeout = 2 * time.Minute

// serviceLinkKinds are the kinds the ServiceLink controller can resolve into a
// pod selector and port. Object storage and the platform control plane are
// deliberately absent: both are always reachable from a workload's baseline
// policy, so neither is ever linked.
var serviceLinkKinds = []string{"ContainerApp", "HfKeyValueCache", "ManagedPostgreSQL", "Trino", "Kafka"}

func NewServiceLinkResource() resource.Resource {
	return &serviceLinkResource{}
}

type serviceLinkResource struct {
	p *providerData
}

type serviceRefModel struct {
	Kind types.String `tfsdk:"kind"`
	Name types.String `tfsdk:"name"`
}

type serviceLinkPortModel struct {
	Port     types.Int64  `tfsdk:"port"`
	Protocol types.String `tfsdk:"protocol"`
}

// The collection and object attributes are framework types, not plain Go slices
// and structs: a config may point them at another resource's computed attribute
// (`target_ports = hyperfluid_container_app.api.ports`), which is unknown at plan
// time, and only the framework types can carry an unknown value.
type serviceLinkModel struct {
	ID          types.String `tfsdk:"id"`
	Env         types.String `tfsdk:"env"`
	Name        types.String `tfsdk:"name"`
	Consumer    types.Object `tfsdk:"consumer"`
	Target      types.Object `tfsdk:"target"`
	TargetPorts types.Set    `tfsdk:"target_ports"`

	// computed
	Ports types.List `tfsdk:"ports"`
	Ready types.Bool `tfsdk:"ready"`
}

func serviceRefType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"kind": types.StringType,
		"name": types.StringType,
	}}
}

func serviceLinkPortType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"port":     types.Int64Type,
		"protocol": types.StringType,
	}}
}

func (r *serviceLinkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_link"
}

// endpointAttribute builds the schema for one side of the link.
func endpointAttribute(desc string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Required:            true,
		MarkdownDescription: desc,
		PlanModifiers:       []planmodifier.Object{objectplanmodifier.RequiresReplace()},
		Attributes: map[string]schema.Attribute{
			"kind": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Kind of service. One of `" + strings.Join(serviceLinkKinds, "`, `") + "`. " +
					"Changing this forces a new link.",
				Validators: []validator.String{stringvalidator.OneOf(serviceLinkKinds...)},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The service's slug — the `slug` attribute of the resource, not its " +
					"display name. Both endpoints must live in `env`. Changing this forces a new link.",
			},
		},
	}
}

func (r *serviceLinkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A declared network path from one service to another within an environment. " +
			"Workloads are isolated by default, so a service is reachable only over the links declared for it.\n\n" +
			"~> **Every configurable attribute forces replacement.** A link is immutable — the API has no " +
			"update endpoint and the underlying resource rejects any change to its endpoints or ports — so " +
			"editing `env`, `consumer`, `target` or `target_ports` destroys the link and creates a new one. " +
			"Reachability is therefore briefly interrupted on such a change.\n\n" +
			"Deleting either endpoint also removes the links that name it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite identifier `env/name`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id both services live in. Changing this forces a new link.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The link's name, derived by the platform from the linked pair " +
					"(`<consumer>-<target>`, truncated to 63 characters). Also its identifier within the environment.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"consumer": endpointAttribute("The service that gets permission to reach `target`. Changing this forces a new link."),
			"target":   endpointAttribute("The service that becomes reachable. Changing this forces a new link."),
			"target_ports": schema.SetNestedAttribute{
				Optional: true,
				MarkdownDescription: "Which of the target's ports to open. Valid only when `target.kind` is " +
					"`ContainerApp` — the one kind that publishes more than one port — and each port must " +
					"already be declared by that app. Omit it to let the platform pick: a container app's " +
					"primary port, or the known port of a fixed-port kind. Changing this forces a new link.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"port": schema.Int64Attribute{Required: true, MarkdownDescription: "Port number."},
						"protocol": schema.StringAttribute{
							Optional: true, Computed: true,
							Default:             stringdefault.StaticString("TCP"),
							MarkdownDescription: "L4 protocol: `TCP`, `UDP` or `SCTP`.",
							Validators:          []validator.String{stringvalidator.OneOf("TCP", "UDP", "SCTP")},
						},
					},
				},
			},
			"ports": schema.ListNestedAttribute{
				Computed: true,
				MarkdownDescription: "The ports actually opened for this link, including the ones the platform " +
					"picked when `target_ports` was omitted. Empty until the link has been reconciled.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"port":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Port number."},
						"protocol": schema.StringAttribute{Computed: true, MarkdownDescription: "L4 protocol."},
					},
				},
			},
			"ready": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the platform has opened the ports for this link.",
			},
		},
	}
}

func (r *serviceLinkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig rejects target ports on a kind that cannot take them, at plan
// time rather than after an apply has already started. Whether the ports exist
// needs the API and is checked in Create.
func (r *serviceLinkResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg serviceLinkModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.TargetPorts.IsNull() || cfg.TargetPorts.IsUnknown() || len(cfg.TargetPorts.Elements()) == 0 {
		return
	}
	target, d := serviceRefFromObject(ctx, cfg.Target)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() || target == nil {
		return
	}
	kind := target.Kind
	if kind.IsNull() || kind.IsUnknown() || kind.ValueString() == "ContainerApp" {
		return
	}
	resp.Diagnostics.AddAttributeError(
		path.Root("target_ports"),
		"Target ports not supported for this kind",
		fmt.Sprintf("target_ports may only be set when target.kind is ContainerApp, got %s. "+
			"Every other kind publishes a single known port, which the platform opens for you — "+
			"drop target_ports.", kind.ValueString()),
	)
}

func (r *serviceLinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceLinkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := plan.Env.ValueString()
	consumerRef, d := serviceRefFromObject(ctx, plan.Consumer)
	resp.Diagnostics.Append(d...)
	targetRef, d := serviceRefFromObject(ctx, plan.Target)
	resp.Diagnostics.Append(d...)
	ports, d := toAPIPorts(ctx, plan.TargetPorts)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() || consumerRef == nil || targetRef == nil {
		return
	}
	target := console.ServiceRef{
		Kind: console.ServiceLinkKind(targetRef.Kind.ValueString()),
		Name: targetRef.Name.ValueString(),
	}

	if len(ports) > 0 {
		if err := r.checkPortsDeclared(ctx, env, target, ports); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("target_ports"), "Target port not published by the app", err.Error())
			return
		}
	}

	body := console.CreateServiceLinkRequestBody{
		Consumer: console.ServiceRef{
			Kind: console.ServiceLinkKind(consumerRef.Kind.ValueString()),
			Name: consumerRef.Name.ValueString(),
		},
		Target: target,
	}
	if len(ports) > 0 {
		body.TargetPorts = &ports
	}

	created, err := r.p.API.CreateServiceLink(ctx, r.p.OrgID, env, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create service link", err.Error())
		return
	}

	// The name is derived by the platform, so it is read off the response rather
	// than recomputed here.
	name := created.Name
	link, err := r.waitReady(ctx, env, name)
	if err != nil {
		resp.Diagnostics.AddError("Service link did not become ready", err.Error())
		return
	}

	state, d := toServiceLinkModel(ctx, env, link)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.TargetPorts = plan.TargetPorts
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *serviceLinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior serviceLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env := prior.Env.ValueString()
	link, err := r.p.API.FindServiceLink(ctx, r.p.OrgID, env, prior.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read service link", err.Error())
		return
	}
	state, d := toServiceLinkModel(ctx, env, link)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// target_ports is a choice, not a reading: the API echoes only the ports it
	// opened, which include the ones it picked itself. Carry the prior value.
	state.TargetPorts = prior.TargetPorts
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is unreachable: every attribute forces replacement.
func (r *serviceLinkResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Service links cannot be updated",
		"A service link is immutable, so every change replaces it. Reaching this means the schema lost a RequiresReplace plan modifier.",
	)
}

func (r *serviceLinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env, name := state.Env.ValueString(), state.Name.ValueString()
	if err := r.p.API.DeleteServiceLink(ctx, r.p.OrgID, env, name); err != nil {
		resp.Diagnostics.AddError("Failed to delete service link", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, serviceLinkWaitTimeout, func() error {
		_, err := r.p.API.FindServiceLink(ctx, r.p.OrgID, env, name)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Service link still present after delete", err.Error())
	}
}

// ImportState parses the "env/name" import id, matching hyperfluid_bucket, the
// other resource the API keys by name rather than by id.
func (r *serviceLinkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	env, name, err := splitEnvName(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("env"), env)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// waitReady polls until the platform reports the link's ports open, so the ports
// attribute is populated by the time the apply returns.
func (r *serviceLinkResource) waitReady(ctx context.Context, env, name string) (*console.ServiceLinkResponse, error) {
	return waitForReady(ctx, serviceLinkWaitTimeout, func() (*console.ServiceLinkResponse, bool, error) {
		link, err := r.p.API.FindServiceLink(ctx, r.p.OrgID, env, name)
		// The listing is served from a read model the operator projects, which
		// trails the create by a moment — absent means "not yet", not "gone".
		if errors.Is(err, client.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return link, link.Ready, nil
	})
}

// checkPortsDeclared reports the first requested port the target app does not
// publish. The API answers that case with a 400 whose body carries no readable
// message, so without this the user gets a bare status code and no way to find
// the port they should have asked for.
func (r *serviceLinkResource) checkPortsDeclared(ctx context.Context, env string, target console.ServiceRef, requested []console.ServiceLinkPort) error {
	declared, err := r.p.API.ContainerAppDeclaredPorts(ctx, r.p.OrgID, env, target.Name)
	if errors.Is(err, client.ErrNotFound) {
		return fmt.Errorf("container app %q not found in this environment — an endpoint takes the app's slug, not its display name", target.Name)
	}
	if err != nil {
		return err
	}
	if want := firstUndeclaredPort(requested, declared); want != nil {
		return fmt.Errorf("container app %q does not publish %s/%d — it publishes %s",
			target.Name, want.Protocol, want.Port, formatPorts(declared))
	}
	return nil
}

// firstUndeclaredPort returns the first requested pair absent from declared, or
// nil. Matching is on the (port, protocol) pair: an app may publish the same
// number under two protocols, so comparing numbers alone would let 53/UDP
// through when only 53/TCP is published.
func firstUndeclaredPort(requested, declared []console.ServiceLinkPort) *console.ServiceLinkPort {
	for i, want := range requested {
		found := false
		for _, have := range declared {
			if have.Port == want.Port && have.Protocol == want.Protocol {
				found = true
				break
			}
		}
		if !found {
			return &requested[i]
		}
	}
	return nil
}

func formatPorts(ports []console.ServiceLinkPort) string {
	if len(ports) == 0 {
		return "no ports"
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, fmt.Sprintf("%s/%d", p.Protocol, p.Port))
	}
	return strings.Join(parts, ", ")
}

// serviceRefFromObject reads one endpoint out of its object value. Returns nil
// when the object is absent or not yet resolved, which a caller must tolerate at
// plan time.
func serviceRefFromObject(ctx context.Context, obj types.Object) (*serviceRefModel, diag.Diagnostics) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}
	var ref serviceRefModel
	d := obj.As(ctx, &ref, basetypes.ObjectAsOptions{})
	if d.HasError() {
		return nil, d
	}
	return &ref, d
}

func toAPIPorts(ctx context.Context, set types.Set) ([]console.ServiceLinkPort, diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, nil
	}
	var models []serviceLinkPortModel
	d := set.ElementsAs(ctx, &models, false)
	if d.HasError() {
		return nil, d
	}
	out := make([]console.ServiceLinkPort, 0, len(models))
	for _, p := range models {
		out = append(out, console.ServiceLinkPort{
			Port:     int32(p.Port.ValueInt64()),
			Protocol: p.Protocol.ValueString(),
		})
	}
	return out, d
}

func portsToList(ctx context.Context, ports []console.ServiceLinkPort) (types.List, diag.Diagnostics) {
	models := make([]serviceLinkPortModel, 0, len(ports))
	for _, p := range ports {
		models = append(models, serviceLinkPortModel{
			Port:     types.Int64Value(int64(p.Port)),
			Protocol: types.StringValue(p.Protocol),
		})
	}
	return types.ListValueFrom(ctx, serviceLinkPortType(), models)
}

func serviceRefToObject(ctx context.Context, kind console.ServiceLinkKind, name string) (types.Object, diag.Diagnostics) {
	return types.ObjectValueFrom(ctx, serviceRefType().AttrTypes, serviceRefModel{
		Kind: types.StringValue(string(kind)),
		Name: types.StringValue(name),
	})
}

// toServiceLinkModel maps the API view into state. target_ports is left to the
// caller: it is config, not something the API reports back.
func toServiceLinkModel(ctx context.Context, env string, link *console.ServiceLinkResponse) (serviceLinkModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	consumer, d := serviceRefToObject(ctx, link.Consumer.Kind, link.Consumer.Name)
	diags.Append(d...)
	target, d := serviceRefToObject(ctx, link.Target.Kind, link.Target.Name)
	diags.Append(d...)
	ports, d := portsToList(ctx, link.Ports)
	diags.Append(d...)

	return serviceLinkModel{
		ID:       types.StringValue(env + "/" + link.Name),
		Env:      types.StringValue(env),
		Name:     types.StringValue(link.Name),
		Consumer: consumer,
		Target:   target,
		Ports:    ports,
		Ready:    types.BoolValue(link.Ready),
	}, diags
}

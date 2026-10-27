// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// principal_access_resource.go — hyperfluid_service_account_access and
// hyperfluid_user_access: the roles, grants and (for a service account)
// attributes of a principal created outside Terraform. Destroying one clears
// the lists it manages and leaves the principal in place.

var (
	_ resource.Resource                = &principalAccessResource{}
	_ resource.ResourceWithConfigure   = &principalAccessResource{}
	_ resource.ResourceWithImportState = &principalAccessResource{}
	_ resource.ResourceWithModifyPlan  = &principalAccessResource{}
)

type accessKind struct {
	typeName      string
	principalType string
	principalAttr string
	description   string
	principalDesc string
	attributes    bool
}

var serviceAccountAccessKind = accessKind{
	typeName:      "_service_account_access",
	principalType: client.PrincipalServiceAccount,
	principalAttr: "service_account_id",
	description:   "The roles, grants and attributes of a service account created in the console or with `hfctl`.",
	principalDesc: "Service account id (its `id`, not its `client_id`).",
	attributes:    true,
}

var userAccessKind = accessKind{
	typeName:      "_user_access",
	principalType: client.PrincipalUser,
	principalAttr: "user_id",
	description:   "The roles and grants of a user. Removing the organization's last owner is refused.",
	principalDesc: "User id.",
}

func NewServiceAccountAccessResource() resource.Resource {
	return &principalAccessResource{kind: serviceAccountAccessKind}
}

func NewUserAccessResource() resource.Resource {
	return &principalAccessResource{kind: userAccessKind}
}

type principalAccessResource struct {
	p    *providerData
	kind accessKind
}

// accessValues are the attributes of either resource, read by path since the
// two schemas differ.
type accessValues struct {
	principal                 types.String
	roles, grants, attributes types.Set
}

type attributeGetter interface {
	GetAttribute(context.Context, path.Path, interface{}) diag.Diagnostics
}

func (r *principalAccessResource) load(ctx context.Context, g attributeGetter) (accessValues, diag.Diagnostics) {
	var v accessValues
	var diags diag.Diagnostics
	diags.Append(g.GetAttribute(ctx, path.Root(r.kind.principalAttr), &v.principal)...)
	diags.Append(g.GetAttribute(ctx, path.Root("roles"), &v.roles)...)
	diags.Append(g.GetAttribute(ctx, path.Root("grants"), &v.grants)...)
	if r.kind.attributes {
		diags.Append(g.GetAttribute(ctx, path.Root("attributes"), &v.attributes)...)
	}
	return v, diags
}

func (r *principalAccessResource) store(ctx context.Context, s *tfsdk.State, v accessValues) diag.Diagnostics {
	var diags diag.Diagnostics
	diags.Append(s.SetAttribute(ctx, path.Root("id"), v.principal)...)
	diags.Append(s.SetAttribute(ctx, path.Root(r.kind.principalAttr), v.principal)...)
	diags.Append(s.SetAttribute(ctx, path.Root("roles"), v.roles)...)
	diags.Append(s.SetAttribute(ctx, path.Root("grants"), v.grants)...)
	if r.kind.attributes {
		diags.Append(s.SetAttribute(ctx, path.Root("attributes"), v.attributes)...)
	}
	return diags
}

func (v accessValues) lists(withAttributes bool) map[string]attr.Value {
	out := map[string]attr.Value{"roles": v.roles, "grants": v.grants}
	if withAttributes {
		out["attributes"] = v.attributes
	}
	return out
}

func (r *principalAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.kind.typeName
}

func (r *principalAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The " + r.kind.principalAttr + ".",
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		r.kind.principalAttr: schema.StringAttribute{
			Required:            true,
			MarkdownDescription: r.kind.principalDesc,
			Validators:          []validator.String{isUUID()},
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"roles":  rolesAttribute(),
		"grants": grantsAttribute(),
	}
	if r.kind.attributes {
		attrs["attributes"] = attributesAttribute()
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: r.kind.description + " Destroying it clears the lists it manages.",
		Attributes:          attrs,
	}
}

func (r *principalAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *principalAccessResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	modifyAccessPlan(ctx, r.p, req, resp)
}

func (r *principalAccessResource) access(id string) principalAccess {
	return principalAccess{api: r.p.API, orgID: r.p.OrgID, ptype: r.kind.principalType, ptID: id}
}

func (r *principalAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	plan, d := r.load(ctx, req.Plan)
	resp.Diagnostics.Append(d...)
	cfg, d := r.load(ctx, req.Config)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	managed, d := managedLists(ctx, nil, cfg.lists(r.kind.attributes))
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.write(ctx, plan, managed, nil, &resp.State)...)
	if resp.Private != nil {
		resp.Diagnostics.Append(recordManaged(ctx, resp.Private, cfg.lists(r.kind.attributes))...)
	}
}

func (r *principalAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	prior, d := r.load(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.read(ctx, prior.principal.ValueString(), roleBindingsFromSet(prior.roles))
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read "+r.kind.principalType+" access", err.Error())
		return
	}
	resp.Diagnostics.Append(r.store(ctx, &resp.State, got)...)
}

func (r *principalAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	plan, d := r.load(ctx, req.Plan)
	resp.Diagnostics.Append(d...)
	cfg, d := r.load(ctx, req.Config)
	resp.Diagnostics.Append(d...)
	state, d := r.load(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	managed, d := managedLists(ctx, req.Private, cfg.lists(r.kind.attributes))
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.write(ctx, plan, managed, roleBindingsFromSet(state.roles), &resp.State)...)
	if resp.Private != nil {
		resp.Diagnostics.Append(recordManaged(ctx, resp.Private, cfg.lists(r.kind.attributes))...)
	}
}

// write applies the managed lists of plan, then stores what the principal
// holds. When the read-back fails, state keeps the plan.
func (r *principalAccessResource) write(ctx context.Context, plan accessValues, managed map[string]bool, prior []roleBinding, state *tfsdk.State) diag.Diagnostics {
	var diags diag.Diagnostics
	id := plan.principal.ValueString()
	if managed["attributes"] {
		attrs, d := setToStringSlice(ctx, plan.attributes)
		diags.Append(d...)
		if err := r.p.API.SetServiceAccountAttributes(ctx, r.p.OrgID, id, attrs); err != nil {
			diags.AddError("Failed to set service account attributes", err.Error())
			managed = nil
		}
	}
	snap, writeErr, readErr := r.access(id).apply(ctx, managed, plan.roles, plan.grants, prior)
	if writeErr != nil {
		diags.AddError("Failed to update "+r.kind.principalType+" access", writeErr.Error())
	}
	got := accessValues{principal: plan.principal}
	if readErr == nil {
		got.attributes, readErr = r.attributes(ctx, id)
	}
	if readErr == nil {
		var d diag.Diagnostics
		got.roles, got.grants, d = snap.sets()
		diags.Append(d...)
	} else {
		diags.AddError("Failed to read "+r.kind.principalType+" access", readErr.Error())
		got = accessValues{principal: plan.principal, roles: knownOrNull(plan.roles), grants: knownOrNull(plan.grants), attributes: knownOrNull(plan.attributes)}
	}
	diags.Append(r.store(ctx, state, got)...)
	return diags
}

// read returns what the principal holds; ErrNotFound when it is gone.
func (r *principalAccessResource) read(ctx context.Context, id string, prior []roleBinding) (accessValues, error) {
	v := accessValues{principal: types.StringValue(id)}
	var err error
	if v.attributes, err = r.attributes(ctx, id); err != nil {
		return v, err
	}
	snap, _, err := r.access(id).apply(ctx, nil, types.SetNull(roleBindingType), types.SetNull(grantEntryType), prior)
	if err != nil {
		return v, err
	}
	var d diag.Diagnostics
	if v.roles, v.grants, d = snap.sets(); d.HasError() {
		return v, fmt.Errorf("convert access to sets: %v", d.Errors())
	}
	return v, nil
}

// attributes reads a service account's attributes; ErrNotFound when it is
// gone. A user has none.
func (r *principalAccessResource) attributes(ctx context.Context, id string) (types.Set, error) {
	if !r.kind.attributes {
		return types.SetNull(types.StringType), nil
	}
	attrs, err := r.p.API.GetServiceAccountAttributes(ctx, r.p.OrgID, id)
	if err != nil {
		return types.SetNull(types.StringType), err
	}
	set, d := stringSliceToSet(ctx, nonNil(attrs))
	if d.HasError() {
		return set, fmt.Errorf("convert attributes to a set: %v", d.Errors())
	}
	return set, nil
}

// Delete clears the lists Terraform manages; the principal stays.
func (r *principalAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	state, d := r.load(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	managed, d := managedAttributes(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	id := state.principal.ValueString()
	if managed["attributes"] {
		if err := r.p.API.SetServiceAccountAttributes(ctx, r.p.OrgID, id, nil); err != nil && !isGone(err) {
			resp.Diagnostics.AddError("Failed to clear service account attributes", err.Error())
			return
		}
	}
	empty := types.SetNull(roleBindingType)
	_, writeErr, readErr := r.access(id).apply(ctx, managed, empty, types.SetNull(grantEntryType), roleBindingsFromSet(state.roles))
	for _, err := range []error{writeErr, readErr} {
		if err != nil && !isGone(err) {
			resp.Diagnostics.AddError("Failed to clear "+r.kind.principalType+" access", err.Error())
			return
		}
	}
}

// ImportState takes the principal id; no list is managed until the
// configuration sets it.
func (r *principalAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root(r.kind.principalAttr), req, resp)
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

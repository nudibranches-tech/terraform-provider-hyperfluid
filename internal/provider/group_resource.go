// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// group_resource.go — an organization group with its members, roles and
// grants.

var (
	_ resource.Resource                = &groupResource{}
	_ resource.ResourceWithConfigure   = &groupResource{}
	_ resource.ResourceWithImportState = &groupResource{}
	_ resource.ResourceWithModifyPlan  = &groupResource{}
)

func NewGroupResource() resource.Resource {
	return &groupResource{}
}

type groupResource struct {
	p *providerData
}

type groupModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Members     types.Set    `tfsdk:"members"`
	Roles       types.Set    `tfsdk:"roles"`
	Grants      types.Set    `tfsdk:"grants"`
}

func (m groupModel) lists() map[string]attr.Value {
	return map[string]attr.Value{"members": m.Members, "roles": m.Roles, "grants": m.Grants}
}

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A group of users and service accounts; its roles and grants apply to every member.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Group id.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Group name, unique within the organization.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form description.",
			},
			"members": membersAttribute(),
			"roles":   rolesAttribute(),
			"grants":  grantsAttribute(),
		},
	}
}

func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *groupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	modifyAccessPlan(ctx, r.p, req, resp)
}

func (r *groupResource) access(groupID string) principalAccess {
	return principalAccess{api: r.p.API, orgID: r.p.OrgID, ptype: client.PrincipalGroup, ptID: groupID}
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.p.API.CreateGroup(ctx, r.p.OrgID, console.CreateGroupRequestBody{
		Name:        plan.Name.ValueString(),
		Description: stringPtr(plan.Description),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to create group", err.Error())
		return
	}
	managed, d := managedLists(ctx, nil, cfg.lists())
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.write(ctx, groupBase(g), plan, managed, nil, &resp.State)...)
	if resp.Private != nil {
		resp.Diagnostics.Append(recordManaged(ctx, resp.Private, cfg.lists())...)
	}
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.p.API.GetGroup(ctx, r.p.OrgID, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read group", err.Error())
		return
	}
	resp.Diagnostics.Append(r.write(ctx, groupBase(g), prior, nil, roleBindingsFromSet(prior.Roles), &resp.State)...)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	base := groupModel{ID: state.ID, Name: state.Name, Description: state.Description}
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		// The update replaces both fields, so a description removed from the
		// configuration is cleared by sending none.
		g, err := r.p.API.UpdateGroup(ctx, r.p.OrgID, state.ID.ValueString(), console.UpdateGroupRequestBody{
			Name:        plan.Name.ValueString(),
			Description: stringPtr(plan.Description),
		})
		if err != nil {
			resp.Diagnostics.AddError("Failed to update group", err.Error())
			return
		}
		base = groupBase(g)
	}
	managed, d := managedLists(ctx, req.Private, cfg.lists())
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.write(ctx, base, plan, managed, roleBindingsFromSet(state.Roles), &resp.State)...)
	if resp.Private != nil {
		resp.Diagnostics.Append(recordManaged(ctx, resp.Private, cfg.lists())...)
	}
}

func groupBase(g *console.Group) groupModel {
	return groupModel{ID: types.StringValue(g.Id.String()), Name: types.StringValue(g.Name), Description: optString(g.Description)}
}

// write applies the managed lists of plan to the group base names, then
// stores what the group holds; with no list managed it only reads. When the
// read-back fails, state keeps the plan so the group is not lost.
func (r *groupResource) write(ctx context.Context, base, plan groupModel, managed map[string]bool, prior []roleBinding, state *tfsdk.State) diag.Diagnostics {
	var diags diag.Diagnostics
	id := base.ID.ValueString()
	if managed["members"] {
		if err := r.syncMembers(ctx, id, membersFromSet(plan.Members)); err != nil {
			diags.AddError("Failed to update group members", err.Error())
			managed = nil
		}
	}
	snap, writeErr, readErr := r.access(id).apply(ctx, managed, plan.Roles, plan.Grants, prior)
	if writeErr != nil {
		diags.AddError("Failed to update group access", writeErr.Error())
	}
	members, err := r.p.API.ListGroupMembers(ctx, r.p.OrgID, id)
	if readErr == nil {
		readErr = err
	}
	m := base
	if readErr != nil {
		diags.AddError("Failed to read group access", readErr.Error())
		m.Members, m.Roles, m.Grants = knownOrNull(plan.Members), knownOrNull(plan.Roles), knownOrNull(plan.Grants)
		diags.Append(state.Set(ctx, m)...)
		return diags
	}
	var d diag.Diagnostics
	m.Roles, m.Grants, d = snap.sets()
	diags.Append(d...)
	ms := make([]member, 0, len(members))
	for _, gm := range members {
		ms = append(ms, member{Type: gm.PrincipalType, ID: gm.PrincipalId.String()})
	}
	m.Members, d = membersToSet(ms)
	diags.Append(d...)
	diags.Append(state.Set(ctx, m)...)
	return diags
}

func (r *groupResource) syncMembers(ctx context.Context, groupID string, desired []member) error {
	current, err := r.p.API.ListGroupMembers(ctx, r.p.OrgID, groupID)
	if err != nil {
		return err
	}
	want := map[member]bool{}
	for _, m := range desired {
		want[m] = true
	}
	have := map[member]bool{}
	for _, gm := range current {
		have[member{Type: gm.PrincipalType, ID: gm.PrincipalId.String()}] = true
	}
	for _, m := range desired {
		if !have[m] {
			if err := r.p.API.AddGroupMember(ctx, r.p.OrgID, groupID, m.Type, m.ID); err != nil {
				return err
			}
		}
	}
	for m := range have {
		if !want[m] {
			if err := r.p.API.RemoveGroupMember(ctx, r.p.OrgID, groupID, m.Type, m.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// Delete deletes the group, its memberships, role bindings and grants.
func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.DeleteGroup(ctx, r.p.OrgID, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to delete group", err.Error())
	}
}

func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

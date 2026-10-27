// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// group_member_resource.go — one user or service account's membership of a
// group. Membership has no id of its own: it is the (group, principal) pair.

var (
	_ resource.Resource                = &groupMemberResource{}
	_ resource.ResourceWithConfigure   = &groupMemberResource{}
	_ resource.ResourceWithImportState = &groupMemberResource{}
)

func NewGroupMemberResource() resource.Resource {
	return &groupMemberResource{}
}

type groupMemberResource struct {
	p *providerData
}

type groupMemberModel struct {
	ID            types.String `tfsdk:"id"`
	GroupID       types.String `tfsdk:"group_id"`
	PrincipalType types.String `tfsdk:"principal_type"`
	PrincipalID   types.String `tfsdk:"principal_id"`
	PrincipalName types.String `tfsdk:"principal_name"`
}

func (r *groupMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_member"
}

func (r *groupMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a user or a service account to a `hyperfluid_group`; the member inherits every " +
			"grant and role bound to the group. Requires `group:read` and `group:update`.\n\n" +
			"A public-client service account cannot be a member: it authenticates as the signed-in user, never as " +
			"itself, so the API refuses it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<group_id>/<principal_type>/<principal_id>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Id of the group. Changing this forces a new membership.",
				Validators:          []validator.String{isUUID()},
				PlanModifiers:       replace,
			},
			"principal_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`user` or `service_account`. Changing this forces a new membership.",
				Validators:          []validator.String{stringvalidator.OneOf(client.PrincipalUser, client.PrincipalServiceAccount)},
				PlanModifiers:       replace,
			},
			"principal_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Id of the user, or of the service account (the `hyperfluid_service_account` data source's " +
					"`id`, not its `client_id`). Users and service accounts are created in the console or with " +
					"hfctl, never by Terraform. Changing this forces a new membership.",
				Validators:    []validator.String{isUUID()},
				PlanModifiers: replace,
			},
			"principal_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user's email, or the service account's name.",
			},
		},
	}
}

func (r *groupMemberResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *groupMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	group, ptype, pid := plan.GroupID.ValueString(), plan.PrincipalType.ValueString(), plan.PrincipalID.ValueString()
	if err := r.p.API.AddGroupMember(ctx, r.p.OrgID, group, ptype, pid); err != nil {
		resp.Diagnostics.AddError("Failed to add group member", err.Error())
		return
	}
	m, err := r.p.API.GetGroupMember(ctx, r.p.OrgID, group, ptype, pid)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read group member after adding it", err.Error())
		return
	}
	plan.ID = types.StringValue(group + "/" + ptype + "/" + pid)
	plan.PrincipalName = types.StringValue(m.PrincipalName)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.p.API.GetGroupMember(ctx, r.p.OrgID, state.GroupID.ValueString(), state.PrincipalType.ValueString(), state.PrincipalID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read group member", err.Error())
		return
	}
	state.PrincipalName = types.StringValue(m.PrincipalName)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is unreachable — every argument forces replacement.
func (r *groupMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan groupMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.RemoveGroupMember(ctx, r.p.OrgID, state.GroupID.ValueString(), state.PrincipalType.ValueString(), state.PrincipalID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to remove group member", err.Error())
	}
}

func (r *groupMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitN(req.ID, 3, "<group_id>/<principal_type>/<principal_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupMemberModel{
		ID:            types.StringValue(req.ID),
		GroupID:       types.StringValue(parts[0]),
		PrincipalType: types.StringValue(parts[1]),
		PrincipalID:   types.StringValue(parts[2]),
		PrincipalName: types.StringNull(),
	})...)
}

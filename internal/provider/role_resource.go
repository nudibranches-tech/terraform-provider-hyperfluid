// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// role_resource.go — a custom role of the organization. Its permission bundle
// is managed by hyperfluid_role_permissions alone, so the two never fight over
// the same list.

var (
	_ resource.Resource                = &roleResource{}
	_ resource.ResourceWithConfigure   = &roleResource{}
	_ resource.ResourceWithImportState = &roleResource{}
)

// systemRoleName matches the names the platform reserves for its system roles,
// compared trimmed and case-insensitively as the API does.
var systemRoleName = regexp.MustCompile(`(?i)^\s*(owner|editor|viewer)\s*$`)

func NewRoleResource() resource.Resource {
	return &roleResource{}
}

type roleResource struct {
	p *providerData
}

type roleModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Color       types.String `tfsdk:"color"`
}

func (r *roleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (r *roleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A custom role of the organization: a named permission bundle to bind to users " +
			"(`hyperfluid_user_role`), groups (`hyperfluid_group_role`) and service accounts " +
			"(`hyperfluid_service_account_role`). Set its permissions with `hyperfluid_role_permissions`, " +
			"which owns the bundle.\n\n" +
			"The system roles (`owner`, `editor`, `viewer`) cannot be created, changed, deleted or imported here; " +
			"look them up with the `hyperfluid_role` data source. Deleting a role removes every binding of it. " +
			"Requires `role:create`, `role:read`, `role:update` and `role:delete`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Role id (uuid).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Role name, unique within the organization. `owner`, `editor` and `viewer` are reserved. Changes in place.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					reservedRoleName{},
				},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form description.",
			},
			"color": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Color the console shows the role with, e.g. `#0f766e`.",
			},
		},
	}
}

func (r *roleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *roleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	role, err := r.p.API.CreateRole(ctx, r.p.OrgID, console.CreateRoleRequestBody{
		Name:        plan.Name.ValueString(),
		Description: stringPtr(plan.Description),
		Color:       stringPtr(plan.Color),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to create role", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, roleToModel(role))...)
}

func (r *roleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior roleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	role, err := r.p.API.GetRole(ctx, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read role", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, roleToModel(role))...)
}

func (r *roleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The update replaces every field, so one removed from the configuration
	// is cleared by sending none.
	role, err := r.p.API.UpdateRole(ctx, state.ID.ValueString(), console.UpdateRoleRequestBody{
		Name:        plan.Name.ValueString(),
		Description: stringPtr(plan.Description),
		Color:       stringPtr(plan.Color),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to update role", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, roleToModel(role))...)
}

func (r *roleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state roleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.DeleteRole(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to delete role", err.Error())
	}
}

// ImportState takes the role id, and refuses a system role: Terraform could
// neither change nor destroy it.
func (r *roleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	role, err := r.p.API.GetRole(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to find the role to import", err.Error())
		return
	}
	if role.IsSystem {
		resp.Diagnostics.AddError("System role cannot be imported",
			"`"+role.Name+"` is a system role, managed by the platform. Reference it with the hyperfluid_role "+
				"data source instead.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func roleToModel(role *console.Role) roleModel {
	return roleModel{
		ID:          types.StringValue(role.Id.String()),
		Name:        types.StringValue(role.Name),
		Description: optString(role.Description),
		Color:       optString(role.Color),
	}
}

// reservedRoleName refuses the system role names at plan time; the API
// answers 422 for them.
type reservedRoleName struct{}

func (reservedRoleName) Description(context.Context) string {
	return "must not be a system role name (owner, editor, viewer)"
}

func (v reservedRoleName) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (reservedRoleName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if systemRoleName.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Reserved role name",
			"`"+req.ConfigValue.ValueString()+"` is a system role; look it up with the hyperfluid_role data source.")
	}
}

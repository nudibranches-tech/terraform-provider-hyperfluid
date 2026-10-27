// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// role_permissions_resource.go — the whole permission bundle of one role,
// managed authoritatively: apply replaces the bundle with exactly the
// configured set in one call, and destroy empties it.

var (
	_ resource.Resource                = &rolePermissionsResource{}
	_ resource.ResourceWithConfigure   = &rolePermissionsResource{}
	_ resource.ResourceWithImportState = &rolePermissionsResource{}
)

func NewRolePermissionsResource() resource.Resource {
	return &rolePermissionsResource{}
}

type rolePermissionsResource struct {
	p *providerData
}

type rolePermissionsModel struct {
	ID          types.String `tfsdk:"id"`
	RoleID      types.String `tfsdk:"role_id"`
	Permissions types.Set    `tfsdk:"permissions"`
}

func (r *rolePermissionsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role_permissions"
}

func (r *rolePermissionsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The permission bundle of a role, managed authoritatively: Terraform makes the role hold " +
			"exactly `permissions`, removing any permission added elsewhere, and destroying the resource empties " +
			"the bundle. It is the only resource that manages a role's permissions (`hyperfluid_role` leaves them " +
			"alone): manage each role with a single `hyperfluid_role_permissions`.\n\n" +
			"System roles are managed by the platform and refuse edits; duplicate one to customize it. " +
			"Requires `role:read` and `role:update`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The role id.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"role_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Id of a custom role (`hyperfluid_role.id`). Changing this moves management to another role.",
				Validators:          []validator.String{isUUID()},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"permissions": schema.SetAttribute{
				ElementType: types.StringType,
				Required:    true,
				MarkdownDescription: "Permission keys the role holds, e.g. `secret:read`. The `hyperfluid_permissions` " +
					"data source lists them. An empty set leaves the role with no permission.",
				Validators: []validator.Set{setvalidator.ValueStringsAre(
					stringvalidator.RegexMatches(permissionKeyPattern, "must be a permission key such as `secret:read`"))},
			},
		},
	}
}

func (r *rolePermissionsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *rolePermissionsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rolePermissionsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, d := r.apply(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
}

func (r *rolePermissionsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan rolePermissionsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, d := r.apply(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
}

// apply replaces the role's bundle with the planned keys and reads it back.
func (r *rolePermissionsResource) apply(ctx context.Context, plan rolePermissionsModel) (rolePermissionsModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	keys, d := setToStringSlice(ctx, plan.Permissions)
	diags.Append(d...)
	if diags.HasError() {
		return plan, diags
	}
	catalog, err := loadPermissionCatalog(ctx, r.p.API)
	if err != nil {
		diags.AddError("Failed to read the permission catalog", err.Error())
		return plan, diags
	}
	ids, err := catalog.ids(keys)
	if err != nil {
		diags.AddAttributeError(path.Root("permissions"), "Unknown permission", err.Error())
		return plan, diags
	}
	roleID := plan.RoleID.ValueString()
	if err := r.p.API.SetRolePermissions(ctx, roleID, ids); err != nil {
		diags.AddError("Failed to set role permissions", err.Error())
		return plan, diags
	}
	got, err := r.read(ctx, roleID)
	if err != nil {
		diags.AddError("Failed to read role permissions after setting them", err.Error())
	}
	return got, diags
}

func (r *rolePermissionsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior rolePermissionsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.read(ctx, prior.RoleID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read role permissions", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
}

func (r *rolePermissionsResource) read(ctx context.Context, roleID string) (rolePermissionsModel, error) {
	entries, err := r.p.API.ListRolePermissions(ctx, roleID)
	if err != nil {
		return rolePermissionsModel{}, err
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	set, d := stringSliceToSet(ctx, keys)
	if d.HasError() {
		return rolePermissionsModel{}, errors.New("convert permissions to a set")
	}
	return rolePermissionsModel{
		ID:          types.StringValue(roleID),
		RoleID:      types.StringValue(roleID),
		Permissions: set,
	}, nil
}

func (r *rolePermissionsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rolePermissionsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.p.API.SetRolePermissions(ctx, state.RoleID.ValueString(), nil)
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to empty role permissions", err.Error())
	}
}

func (r *rolePermissionsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("role_id"), req, resp)
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

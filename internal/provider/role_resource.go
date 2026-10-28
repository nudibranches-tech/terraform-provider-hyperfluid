// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"regexp"

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

// role_resource.go — a custom role of the organization and its permission
// bundle.

var (
	_ resource.Resource                = &roleResource{}
	_ resource.ResourceWithConfigure   = &roleResource{}
	_ resource.ResourceWithImportState = &roleResource{}
	_ resource.ResourceWithModifyPlan  = &roleResource{}
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
	Permissions types.Set    `tfsdk:"permissions"`
}

func (m roleModel) lists() map[string]attr.Value {
	return map[string]attr.Value{"permissions": m.Permissions}
}

func (r *roleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (r *roleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A custom role: a named set of permissions. Deleting it removes every binding of it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Role id (uuid).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Role name, unique within the organization. `owner`, `editor` and `viewer` are reserved.",
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
			"permissions": permissionsAttribute(),
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

func (r *roleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var perms types.Set
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("permissions"), &perms)...)
	keys, d := setToStringSlice(ctx, perms)
	resp.Diagnostics.Append(d...)
	checkPermissionKeys(ctx, r.p, path.Root("permissions"), keys, &resp.Diagnostics)
}

func (r *roleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
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
	r.write(ctx, role, plan, cfg, nil, &resp.State, &resp.Diagnostics)
	if resp.Private != nil {
		resp.Diagnostics.Append(recordManaged(ctx, resp.Private, cfg.lists())...)
	}
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
	m, err := r.read(ctx, role)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read role permissions", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *roleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
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
	r.write(ctx, role, plan, cfg, req.Private, &resp.State, &resp.Diagnostics)
	if resp.Private != nil {
		resp.Diagnostics.Append(recordManaged(ctx, resp.Private, cfg.lists())...)
	}
}

// write sets the permission bundle when it is managed, then stores what the
// role holds.
func (r *roleResource) write(ctx context.Context, role *console.Role, plan, cfg roleModel, prior privateGetter, state *tfsdk.State, diags *diag.Diagnostics) {
	managed, d := managedLists(ctx, prior, cfg.lists())
	diags.Append(d...)
	if managed["permissions"] {
		if err := r.setPermissions(ctx, role.Id.String(), plan.Permissions); err != nil {
			diags.AddError("Failed to set role permissions", err.Error())
		}
	}
	m, err := r.read(ctx, role)
	if err != nil {
		diags.AddError("Failed to read role permissions", err.Error())
		m = roleToModel(role)
		m.Permissions = types.SetNull(types.StringType)
		if !plan.Permissions.IsUnknown() {
			m.Permissions = plan.Permissions
		}
	}
	diags.Append(state.Set(ctx, m)...)
}

func (r *roleResource) setPermissions(ctx context.Context, roleID string, perms types.Set) error {
	keys, d := setToStringSlice(ctx, perms)
	if d.HasError() {
		return errors.New("read the planned permissions")
	}
	catalog, err := loadPermissionCatalog(ctx, r.p.API)
	if err != nil {
		return err
	}
	ids, err := catalog.ids(keys)
	if err != nil {
		return err
	}
	return r.p.API.SetRolePermissions(ctx, roleID, ids)
}

func (r *roleResource) read(ctx context.Context, role *console.Role) (roleModel, error) {
	m := roleToModel(role)
	entries, err := r.p.API.ListRolePermissions(ctx, role.Id.String())
	if err != nil {
		return m, err
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	set, d := stringSliceToSet(ctx, keys)
	if d.HasError() {
		return m, errors.New("convert permissions to a set")
	}
	m.Permissions = set
	return m, nil
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
		Permissions: types.SetNull(types.StringType),
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

// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// grant_resource.go — one fine-grained permission granted to (or, with
// effect = deny, withdrawn from) a user, a service account or a group. Grants
// are immutable and have no GET-by-id route: Read lists the principal's grants
// and matches the id.

var (
	_ resource.Resource                   = &grantResource{}
	_ resource.ResourceWithConfigure      = &grantResource{}
	_ resource.ResourceWithImportState    = &grantResource{}
	_ resource.ResourceWithValidateConfig = &grantResource{}
)

func NewGrantResource() resource.Resource {
	return &grantResource{}
}

type grantResource struct {
	p *providerData
}

type grantModel struct {
	ID            types.String `tfsdk:"id"`
	PrincipalType types.String `tfsdk:"principal_type"`
	PrincipalID   types.String `tfsdk:"principal_id"`
	Permission    types.String `tfsdk:"permission"`
	PermissionID  types.String `tfsdk:"permission_id"`
	ScopePath     types.String `tfsdk:"scope_path"`
	ResourceUUID  types.String `tfsdk:"resource_uuid"`
	Effect        types.String `tfsdk:"effect"`
}

func (r *grantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_grant"
}

func (r *grantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A fine-grained permission granted to a user, a service account or a group, " +
			"organization-wide or narrowed by `scope_path`. `effect = \"deny\"` withdraws the permission instead, " +
			"with precedence over every allow. Grants are immutable: every argument forces a new grant.\n\n" +
			"Requires `grant:create`, `grant:read` and `grant:delete`. The same permission cannot be granted twice " +
			"to one principal at one scope with one effect.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Grant id (uuid).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"principal_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`user`, `service_account` or `group`.",
				Validators: []validator.String{stringvalidator.OneOf(
					client.PrincipalUser, client.PrincipalServiceAccount, client.PrincipalGroup)},
				PlanModifiers: replace,
			},
			"principal_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Id of the user, the service account (the `hyperfluid_service_account` data source's `id`, not its " +
					"`client_id`) or the group. A public-client service account cannot hold grants: the API refuses it.",
				Validators:    []validator.String{isUUID()},
				PlanModifiers: replace,
			},
			"permission": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Permission key, `<resource_type>:<action>`, e.g. `secret:read`. The " +
					"`hyperfluid_permissions` data source lists them.",
				Validators:    []validator.String{stringvalidator.RegexMatches(permissionKeyPattern, "must be a permission key such as `secret:read`")},
				PlanModifiers: replace,
			},
			"permission_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Id of the permission on this installation.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"scope_path": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Path in the organization's resource tree, within the permission's resource family: " +
					"exact, an ancestor folder (covers what is beneath it), or a `*`-suffixed wildcard at a path " +
					"boundary (`prod/*`). Omit for an organization-wide grant.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: replace,
			},
			"resource_uuid": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Id of the one resource `scope_path` names exactly. The grant then survives a rename " +
					"of the resource and is deleted with it. Requires an exact `scope_path`.",
				Validators:    []validator.String{isUUID()},
				PlanModifiers: replace,
			},
			"effect": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(string(console.EffectAllow)),
				MarkdownDescription: "`allow` (default) or `deny`.",
				Validators:          []validator.String{stringvalidator.OneOf(string(console.EffectAllow), string(console.EffectDeny))},
				PlanModifiers:       replace,
			},
		},
	}
}

// ValidateConfig mirrors the API's rule that an anchored grant names one
// concrete resource.
func (r *grantResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var scope, uuid types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("scope_path"), &scope)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("resource_uuid"), &uuid)...)
	if resp.Diagnostics.HasError() || scope.IsUnknown() || uuid.IsNull() {
		return
	}
	if scope.IsNull() || strings.Contains(scope.ValueString(), "*") {
		resp.Diagnostics.AddAttributeError(path.Root("scope_path"), "Exact scope required",
			"`resource_uuid` anchors the grant to one resource, so `scope_path` must be that resource's exact path, "+
				"without a wildcard.")
	}
}

func (r *grantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *grantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan grantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	catalog, err := loadPermissionCatalog(ctx, r.p.API)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the permission catalog", err.Error())
		return
	}
	ids, err := catalog.ids([]string{plan.Permission.ValueString()})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("permission"), "Unknown permission", err.Error())
		return
	}
	effect := console.Effect(plan.Effect.ValueString())
	body := console.CreateGrantRequestBody{
		PermissionId: ids[0],
		Effect:       &effect,
	}
	if s := strings.TrimSpace(plan.ScopePath.ValueString()); s != "" {
		body.ScopePath = &s
	}
	if !plan.ResourceUUID.IsNull() {
		u, err := uuidFromString(plan.ResourceUUID.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("resource_uuid"), "Invalid resource_uuid", err.Error())
			return
		}
		body.ResourceUuid = &u
	}
	g, err := r.p.API.CreateGrant(ctx, r.p.OrgID, plan.PrincipalType.ValueString(), plan.PrincipalID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create grant", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, grantToModel(g, catalog))...)
}

func (r *grantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior grantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.p.API.GetGrant(ctx, r.p.OrgID, prior.PrincipalType.ValueString(), prior.PrincipalID.ValueString(), prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read grant", err.Error())
		return
	}
	if g.PermissionId == nil {
		resp.Diagnostics.AddError("Not a permission grant",
			"grant "+prior.ID.ValueString()+" binds a role; manage it with hyperfluid_group_role or hyperfluid_service_account_role")
		return
	}
	catalog, err := loadPermissionCatalog(ctx, r.p.API)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the permission catalog", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, grantToModel(g, catalog))...)
}

// Update is unreachable — every argument forces replacement.
func (r *grantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior grantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	plan.PermissionID = prior.PermissionID
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *grantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state grantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.DeleteGrant(ctx, r.p.OrgID, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to delete grant", err.Error())
	}
}

// ImportState takes `<principal_type>/<principal_id>/<grant_id>`: reading a
// grant back needs its principal.
func (r *grantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitN(req.ID, 3, "<principal_type>/<principal_id>/<grant_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal_type"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

func grantToModel(g *console.Grant, catalog *permissionCatalog) grantModel {
	m := grantModel{
		ID:            types.StringValue(g.Id.String()),
		PrincipalType: types.StringValue(g.PrincipalType),
		PrincipalID:   types.StringValue(g.PrincipalId.String()),
		Permission:    types.StringNull(),
		PermissionID:  types.StringNull(),
		ScopePath:     optString(g.ScopePath),
		ResourceUUID:  types.StringNull(),
		Effect:        types.StringValue(g.Effect),
	}
	if g.PermissionId != nil {
		m.Permission = types.StringValue(catalog.key(*g.PermissionId))
		m.PermissionID = types.StringValue(g.PermissionId.String())
	}
	if g.ResourceUuid != nil {
		m.ResourceUUID = types.StringValue(g.ResourceUuid.String())
	}
	return m
}

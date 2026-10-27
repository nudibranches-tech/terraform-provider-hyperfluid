// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// role_binding_resource.go — hyperfluid_group_role,
// hyperfluid_service_account_role and hyperfluid_user_role: a role bound to one
// principal, organization-wide or at a scope. A binding has no id of its own:
// the API stores it as a grant carrying the role (and, for a user's
// organization-wide role, as a row of the user's roles), so it is read back by
// listing those.

var (
	_ resource.Resource                   = &roleBindingResource{}
	_ resource.ResourceWithConfigure      = &roleBindingResource{}
	_ resource.ResourceWithImportState    = &roleBindingResource{}
	_ resource.ResourceWithValidateConfig = &roleBindingResource{}
)

// roleBindingFields are the attributes the binding resources share; each
// resource adds the attribute naming its principal.
type roleBindingFields struct {
	ID           types.String `tfsdk:"id"`
	RoleID       types.String `tfsdk:"role_id"`
	ScopePath    types.String `tfsdk:"scope_path"`
	ResourceUUID types.String `tfsdk:"resource_uuid"`
	ResourceType types.String `tfsdk:"resource_type"`
}

type groupRoleModel struct {
	GroupID types.String `tfsdk:"group_id"`
	roleBindingFields
}

type serviceAccountRoleModel struct {
	ServiceAccountID types.String `tfsdk:"service_account_id"`
	roleBindingFields
}

type userRoleModel struct {
	UserID types.String `tfsdk:"user_id"`
	roleBindingFields
}

type stateGetter interface {
	Get(ctx context.Context, target interface{}) diag.Diagnostics
}

// roleBindingKind is what differs between the binding resources.
type roleBindingKind struct {
	typeName      string
	principalType string
	principalAttr string
	principalDesc string
	permissions   string
	load          func(context.Context, stateGetter) (types.String, roleBindingFields, diag.Diagnostics)
	store         func(context.Context, *tfsdk.State, types.String, roleBindingFields) diag.Diagnostics
	// exists returns nil when the binding is in place, else ErrNotFound; nil
	// means the principal's grants are searched.
	exists func(ctx context.Context, api *client.Client, orgID, principal, roleID string, scope client.RoleScope) error
}

var groupRoleKind = roleBindingKind{
	typeName:      "_group_role",
	principalType: client.PrincipalGroup,
	principalAttr: "group_id",
	principalDesc: "Id of the group (`hyperfluid_group.id`).",
	permissions:   "Requires `grant:create`, `grant:read` and `grant:delete`.",
	load: func(ctx context.Context, g stateGetter) (types.String, roleBindingFields, diag.Diagnostics) {
		var m groupRoleModel
		d := g.Get(ctx, &m)
		return m.GroupID, m.roleBindingFields, d
	},
	store: func(ctx context.Context, s *tfsdk.State, principal types.String, f roleBindingFields) diag.Diagnostics {
		return s.Set(ctx, groupRoleModel{GroupID: principal, roleBindingFields: f})
	},
}

var serviceAccountRoleKind = roleBindingKind{
	typeName:      "_service_account_role",
	principalType: client.PrincipalServiceAccount,
	principalAttr: "service_account_id",
	principalDesc: "Id of the service account (the `hyperfluid_service_account` data source's `id`, not its " +
		"`client_id`); service accounts are created in the console or with hfctl. " +
		"A public-client account cannot hold a role: the API refuses it.",
	permissions: "Requires `service_account:update` and `grant:read`.",
	load: func(ctx context.Context, g stateGetter) (types.String, roleBindingFields, diag.Diagnostics) {
		var m serviceAccountRoleModel
		d := g.Get(ctx, &m)
		return m.ServiceAccountID, m.roleBindingFields, d
	},
	store: func(ctx context.Context, s *tfsdk.State, principal types.String, f roleBindingFields) diag.Diagnostics {
		return s.Set(ctx, serviceAccountRoleModel{ServiceAccountID: principal, roleBindingFields: f})
	},
}

var userRoleKind = roleBindingKind{
	typeName:      "_user_role",
	principalType: client.PrincipalUser,
	principalAttr: "user_id",
	principalDesc: "Id of the user; users are created in the console, never by Terraform.",
	permissions: "Requires `user:update`, plus `user:read` for an organization-wide binding and `grant:read` " +
		"for a scoped one. Unbinding the organization's last owner is refused.",
	load: func(ctx context.Context, g stateGetter) (types.String, roleBindingFields, diag.Diagnostics) {
		var m userRoleModel
		d := g.Get(ctx, &m)
		return m.UserID, m.roleBindingFields, d
	},
	store: func(ctx context.Context, s *tfsdk.State, principal types.String, f roleBindingFields) diag.Diagnostics {
		return s.Set(ctx, userRoleModel{UserID: principal, roleBindingFields: f})
	},
	// A user's organization-wide roles are a list of their own, which also
	// holds the roles assigned at sign-up or by invitation; scoped ones are
	// grants.
	exists: func(ctx context.Context, api *client.Client, orgID, user, roleID string, scope client.RoleScope) error {
		if scope.ScopePath != "" {
			_, err := api.FindRoleBinding(ctx, orgID, client.PrincipalUser, user, roleID, scope)
			return err
		}
		roles, err := api.ListUserRoles(ctx, orgID, user)
		if err != nil {
			return err
		}
		for _, r := range roles {
			if r.Id.String() == roleID {
				return nil
			}
		}
		return client.ErrNotFound
	},
}

func NewUserRoleResource() resource.Resource {
	return &roleBindingResource{kind: userRoleKind}
}

func NewGroupRoleResource() resource.Resource {
	return &roleBindingResource{kind: groupRoleKind}
}

func NewServiceAccountRoleResource() resource.Resource {
	return &roleBindingResource{kind: serviceAccountRoleKind}
}

type roleBindingResource struct {
	p    *providerData
	kind roleBindingKind
}

func (r *roleBindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.kind.typeName
}

func (r *roleBindingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Binds a role's permission bundle to a " + strings.ReplaceAll(r.kind.principalType, "_", " ") +
			", organization-wide or narrowed by `scope_path`. Every argument forces a new binding. " +
			r.kind.permissions + "\n\n" +
			"`resource_type` cannot be read back from the API: it is kept as configured, and is null after import.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<" + r.kind.principalAttr + ">/<role_id>[/<scope_path>]`, the import id.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			r.kind.principalAttr: schema.StringAttribute{
				Required:            true,
				MarkdownDescription: r.kind.principalDesc,
				Validators:          []validator.String{isUUID()},
				PlanModifiers:       replace,
			},
			"role_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Id of a role of the organization (`hyperfluid_role.id`, or the `hyperfluid_role` data source for a system role such as `viewer`).",
				Validators:          []validator.String{isUUID()},
				PlanModifiers:       replace,
			},
			"scope_path": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Path in the organization's resource tree the binding applies to: exact, an ancestor " +
					"folder (covers what is beneath it), or a `*`-suffixed wildcard at a path boundary (`prod/*`). " +
					"Omit for an organization-wide binding.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: replace,
			},
			"resource_uuid": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Id of the one resource `scope_path` names exactly. The binding then survives a rename " +
					"of the resource and is removed with it. Requires an exact `scope_path`.",
				Validators:    []validator.String{isUUID()},
				PlanModifiers: replace,
			},
			"resource_type": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Resource family of the bound resource, e.g. `secret`: only the role's permissions of " +
					"that family apply. Requires an exact `scope_path`.",
				PlanModifiers: replace,
			},
		},
	}
}

// ValidateConfig mirrors the API's rule that an anchored binding names one
// concrete resource: an exact scope_path, never a wildcard or org-wide.
func (r *roleBindingResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var scope, uuid, family types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("scope_path"), &scope)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("resource_uuid"), &uuid)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("resource_type"), &family)...)
	if resp.Diagnostics.HasError() || scope.IsUnknown() {
		return
	}
	anchored := !uuid.IsNull() || !family.IsNull()
	if anchored && (scope.IsNull() || strings.Contains(scope.ValueString(), "*")) {
		resp.Diagnostics.AddAttributeError(path.Root("scope_path"), "Exact scope required",
			"`resource_uuid` and `resource_type` bind one concrete resource, so `scope_path` must be that "+
				"resource's exact path, without a wildcard.")
	}
}

func (r *roleBindingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// bindingID is the composite id of a binding, also its import id.
func bindingID(principal string, f roleBindingFields) types.String {
	id := principal + "/" + f.RoleID.ValueString()
	if s := strings.TrimSpace(f.ScopePath.ValueString()); s != "" {
		id += "/" + s
	}
	return types.StringValue(id)
}

func (r *roleBindingResource) exists(ctx context.Context, principal string, f roleBindingFields) error {
	if r.kind.exists != nil {
		return r.kind.exists(ctx, r.p.API, r.p.OrgID, principal, f.RoleID.ValueString(), roleScope(f))
	}
	_, err := r.p.API.FindRoleBinding(ctx, r.p.OrgID, r.kind.principalType, principal, f.RoleID.ValueString(), roleScope(f))
	return err
}

func roleScope(f roleBindingFields) client.RoleScope {
	return client.RoleScope{
		ScopePath:    strings.TrimSpace(f.ScopePath.ValueString()),
		ResourceUUID: f.ResourceUUID.ValueString(),
		ResourceType: f.ResourceType.ValueString(),
	}
}

func (r *roleBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	principal, f, d := r.kind.load(ctx, req.Plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	scope := roleScope(f)
	if err := r.p.API.BindRole(ctx, r.p.OrgID, r.kind.principalType, principal.ValueString(), f.RoleID.ValueString(), scope); err != nil {
		resp.Diagnostics.AddError("Failed to bind role", err.Error())
		return
	}
	if err := r.exists(ctx, principal.ValueString(), f); err != nil {
		resp.Diagnostics.AddError("Failed to read role binding after creating it", err.Error())
		return
	}
	f.ID = bindingID(principal.ValueString(), f)
	resp.Diagnostics.Append(r.kind.store(ctx, &resp.State, principal, f)...)
}

func (r *roleBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	principal, f, d := r.kind.load(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.exists(ctx, principal.ValueString(), f)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read role binding", err.Error())
		return
	}
	f.ID = bindingID(principal.ValueString(), f)
	resp.Diagnostics.Append(r.kind.store(ctx, &resp.State, principal, f)...)
}

// Update is unreachable — every argument forces replacement.
func (r *roleBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	principal, f, d := r.kind.load(ctx, req.Plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, prior, d := r.kind.load(ctx, req.State)
	resp.Diagnostics.Append(d...)
	f.ID = prior.ID
	resp.Diagnostics.Append(r.kind.store(ctx, &resp.State, principal, f)...)
}

func (r *roleBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	principal, f, d := r.kind.load(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.UnbindRole(ctx, r.p.OrgID, r.kind.principalType, principal.ValueString(), f.RoleID.ValueString(), roleScope(f)); err != nil {
		resp.Diagnostics.AddError("Failed to unbind role", err.Error())
	}
}

// ImportState takes `<principal_id>/<role_id>` for an organization-wide binding,
// or `<principal_id>/<role_id>/<scope_path>`; the scope path may itself
// contain slashes.
func (r *roleBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	form := "<" + r.kind.principalAttr + ">/<role_id>[/<scope_path>]"
	parts := strings.SplitN(req.ID, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" || (len(parts) == 3 && parts[2] == "") {
		resp.Diagnostics.AddError("Invalid import id", "expected import id in the form \""+form+"\", got \""+req.ID+"\"")
		return
	}
	scope := types.StringNull()
	if len(parts) == 3 {
		scope = types.StringValue(parts[2])
	}
	principal := types.StringValue(parts[0])
	f := roleBindingFields{
		RoleID:       types.StringValue(parts[1]),
		ScopePath:    scope,
		ResourceUUID: types.StringNull(),
		ResourceType: types.StringNull(),
	}
	f.ID = bindingID(parts[0], f)
	// An organization-wide binding has no resource_uuid; the Read that follows
	// the import checks it exists.
	if scope.IsNull() {
		resp.Diagnostics.Append(r.kind.store(ctx, &resp.State, principal, f)...)
		return
	}
	// The import id cannot carry resource_uuid, so it is taken from the grant.
	grants, err := r.p.API.ListGrants(ctx, r.p.OrgID, r.kind.principalType, parts[0])
	if err != nil {
		resp.Diagnostics.AddError("Failed to find the role binding to import", err.Error())
		return
	}
	var matches []console.Grant
	for _, g := range grants {
		if g.PermissionId == nil && g.RoleId != nil && g.RoleId.String() == parts[1] &&
			optString(g.ScopePath).Equal(scope) {
			matches = append(matches, g)
		}
	}
	switch len(matches) {
	case 0:
		resp.Diagnostics.AddError("Role binding not found",
			"no binding of role "+parts[1]+" at that scope on "+r.kind.principalAttr+" "+parts[0])
		return
	case 1:
	default:
		resp.Diagnostics.AddError("Ambiguous role binding",
			"the role is bound several times at that scope, to different resources; import is not supported for it")
		return
	}
	if u := matches[0].ResourceUuid; u != nil {
		f.ResourceUUID = types.StringValue(u.String())
	}
	resp.Diagnostics.Append(r.kind.store(ctx, &resp.State, principal, f)...)
}

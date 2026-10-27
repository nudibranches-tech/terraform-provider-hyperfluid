// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// iam_access.go — the IAM lists a role, a group, a service account or a user
// carries: permissions, members, role bindings, grants and attributes. A list
// the configuration leaves out is not managed (see unsetSet); a list it sets
// is authoritative.

// roleBinding is a role bound to a principal. ResourceType is never returned
// by the API, so it is carried from state.
type roleBinding struct {
	RoleID, ScopePath, ResourceUUID, ResourceType string
}

// key identifies a binding the way unbinding does: by role and scope only.
func (b roleBinding) key() [2]string { return [2]string{b.RoleID, b.ScopePath} }

func (b roleBinding) scope() client.RoleScope {
	return client.RoleScope{ScopePath: b.ScopePath, ResourceUUID: b.ResourceUUID, ResourceType: b.ResourceType}
}

type grantEntry struct {
	Permission, ScopePath, ResourceUUID, Effect string
}

type member struct {
	Type, ID string
}

// ── schema ────────────────────────────────────────────────────────────────

var (
	roleBindingType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"role_id": types.StringType, "scope_path": types.StringType,
		"resource_uuid": types.StringType, "resource_type": types.StringType,
	}}
	grantEntryType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"permission": types.StringType, "scope_path": types.StringType,
		"resource_uuid": types.StringType, "effect": types.StringType,
	}}
	memberType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"type": types.StringType, "id": types.StringType,
	}}
)

var (
	trimmedNonEmpty                = regexp.MustCompile(`^\S(.*\S)?$`)
	serviceAccountAttributePattern = regexp.MustCompile(`^[^:]+::.+$`)
)

const unmanagedNote = " Left out, it is not managed; once set, it is authoritative and removing it clears it."

func scopePathAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Optional:            true,
		MarkdownDescription: "Resource path, exact or a wildcard such as `prod/*`. Omit for organization-wide.",
		Validators:          []validator.String{stringvalidator.RegexMatches(trimmedNonEmpty, "must be a non-blank path without surrounding spaces")},
	}
}

func resourceUUIDAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Optional:            true,
		MarkdownDescription: "Id of the resource an exact `scope_path` names, e.g. `hyperfluid_secret.x.id`.",
		Validators:          []validator.String{isUUID()},
	}
}

func rolesAttribute() schema.SetNestedAttribute {
	return schema.SetNestedAttribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Roles bound to this principal." + unmanagedNote,
		PlanModifiers:       []planmodifier.Set{unsetSet("roles")},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"role_id": schema.StringAttribute{
					Required: true, MarkdownDescription: "Role id.", Validators: []validator.String{isUUID()},
				},
				"scope_path":    scopePathAttribute(),
				"resource_uuid": resourceUUIDAttribute(),
				"resource_type": schema.StringAttribute{
					Optional:            true,
					MarkdownDescription: "Limits the role to its permissions on this resource type; needs an exact `scope_path`.",
					Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
				},
			},
			Validators: []validator.Object{exactScope{}},
		},
	}
}

func grantsAttribute() schema.SetNestedAttribute {
	return schema.SetNestedAttribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Permissions granted directly to this principal." + unmanagedNote,
		PlanModifiers:       []planmodifier.Set{unsetSet("grants")},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"permission": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "Permission key, e.g. `secret:read`.",
					Validators:          []validator.String{stringvalidator.RegexMatches(permissionKeyPattern, "must be a permission key such as `secret:read`")},
				},
				"scope_path":    scopePathAttribute(),
				"resource_uuid": resourceUUIDAttribute(),
				"effect": schema.StringAttribute{
					Optional: true, Computed: true,
					Default:             stringdefault.StaticString(string(console.EffectAllow)),
					MarkdownDescription: "`allow` (default) or `deny`.",
					Validators:          []validator.String{stringvalidator.OneOf(string(console.EffectAllow), string(console.EffectDeny))},
				},
			},
			Validators: []validator.Object{exactScope{}},
		},
	}
}

func membersAttribute() schema.SetNestedAttribute {
	return schema.SetNestedAttribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Users and service accounts in the group." + unmanagedNote,
		PlanModifiers:       []planmodifier.Set{unsetSet("members")},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"type": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "`user` or `service_account`.",
					Validators:          []validator.String{stringvalidator.OneOf(client.PrincipalUser, client.PrincipalServiceAccount)},
				},
				"id": schema.StringAttribute{
					Required: true, MarkdownDescription: "User or service account id.", Validators: []validator.String{isUUID()},
				},
			},
		},
	}
}

func permissionsAttribute() schema.SetAttribute {
	return schema.SetAttribute{
		ElementType: types.StringType,
		Optional:    true, Computed: true,
		MarkdownDescription: "Permission keys, e.g. `secret:read`." + unmanagedNote,
		PlanModifiers:       []planmodifier.Set{unsetSet("permissions")},
		Validators: []validator.Set{setvalidator.ValueStringsAre(
			stringvalidator.RegexMatches(permissionKeyPattern, "must be a permission key such as `secret:read`"))},
	}
}

func attributesAttribute() schema.SetAttribute {
	return schema.SetAttribute{
		ElementType: types.StringType,
		Optional:    true, Computed: true,
		MarkdownDescription: "Row-level-security attributes, `namespace::value`." + unmanagedNote,
		PlanModifiers:       []planmodifier.Set{unsetSet("attributes")},
		Validators: []validator.Set{setvalidator.ValueStringsAre(
			stringvalidator.RegexMatches(serviceAccountAttributePattern, "must be in `namespace::value` form"))},
	}
}

// exactScope mirrors the API: resource_uuid and resource_type name one
// concrete resource, so scope_path must be its exact path.
type exactScope struct{}

func (exactScope) Description(context.Context) string {
	return "resource_uuid and resource_type require an exact scope_path"
}

func (v exactScope) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (exactScope) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	attrs := req.ConfigValue.Attributes()
	scope, _ := attrs["scope_path"].(types.String)
	if scope.IsUnknown() {
		return
	}
	for _, name := range []string{"resource_uuid", "resource_type"} {
		v, ok := attrs[name].(types.String)
		if !ok || v.IsNull() {
			continue
		}
		if scope.IsNull() || strings.Contains(scope.ValueString(), "*") {
			resp.Diagnostics.AddAttributeError(req.Path.AtName("scope_path"), "Exact scope required",
				"`"+name+"` names one concrete resource, so `scope_path` must be that resource's exact path, without a wildcard.")
			return
		}
	}
}

// ── lists the configuration leaves out ────────────────────────────────────

// unsetSet is unsetList for a set.
func unsetSet(attribute string) planmodifier.Set { return unsetSetModifier{attribute: attribute} }

type unsetSetModifier struct{ attribute string }

func (unsetSetModifier) Description(context.Context) string {
	return "Keeps the current value when the configuration never set it, and clears it once the configuration stops setting it."
}

func (m unsetSetModifier) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m unsetSetModifier) PlanModifySet(ctx context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse) {
	if !req.ConfigValue.IsNull() || req.StateValue.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	managed, d := managedAttributes(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	resp.PlanValue = req.StateValue
	if managed[m.attribute] {
		resp.PlanValue = types.SetValueMust(req.StateValue.ElementType(ctx), []attr.Value{})
	}
}

// managedLists returns the lists to write in this apply: those the
// configuration sets, and those it stopped setting since the last apply.
func managedLists(ctx context.Context, prior privateGetter, lists map[string]attr.Value) (map[string]bool, diag.Diagnostics) {
	var before map[string]bool
	var d diag.Diagnostics
	if prior != nil {
		before, d = managedAttributes(ctx, prior)
	}
	out := map[string]bool{}
	for name, v := range lists {
		out[name] = !v.IsNull() || before[name]
	}
	return out, d
}

// ── conversions ───────────────────────────────────────────────────────────

func strOf(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func strOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func setObjects[T any](s types.Set, conv func(map[string]attr.Value) T) []T {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	out := make([]T, 0, len(s.Elements()))
	for _, e := range s.Elements() {
		if o, ok := e.(types.Object); ok {
			out = append(out, conv(o.Attributes()))
		}
	}
	return out
}

func attrStr(attrs map[string]attr.Value, name string) string {
	s, _ := attrs[name].(types.String)
	return strOf(s)
}

func roleBindingsFromSet(s types.Set) []roleBinding {
	return setObjects(s, func(a map[string]attr.Value) roleBinding {
		return roleBinding{attrStr(a, "role_id"), strings.TrimSpace(attrStr(a, "scope_path")), attrStr(a, "resource_uuid"), attrStr(a, "resource_type")}
	})
}

func grantEntriesFromSet(s types.Set) []grantEntry {
	return setObjects(s, func(a map[string]attr.Value) grantEntry {
		effect := attrStr(a, "effect")
		if effect == "" {
			effect = string(console.EffectAllow)
		}
		return grantEntry{attrStr(a, "permission"), strings.TrimSpace(attrStr(a, "scope_path")), attrStr(a, "resource_uuid"), effect}
	})
}

func membersFromSet(s types.Set) []member {
	return setObjects(s, func(a map[string]attr.Value) member {
		return member{attrStr(a, "type"), attrStr(a, "id")}
	})
}

func roleBindingsToSet(bs []roleBinding) (types.Set, diag.Diagnostics) {
	elems := make([]attr.Value, 0, len(bs))
	for _, b := range bs {
		elems = append(elems, types.ObjectValueMust(roleBindingType.AttrTypes, map[string]attr.Value{
			"role_id": types.StringValue(b.RoleID), "scope_path": strOrNull(b.ScopePath),
			"resource_uuid": strOrNull(b.ResourceUUID), "resource_type": strOrNull(b.ResourceType),
		}))
	}
	return types.SetValue(roleBindingType, elems)
}

func grantEntriesToSet(gs []grantEntry) (types.Set, diag.Diagnostics) {
	elems := make([]attr.Value, 0, len(gs))
	for _, g := range gs {
		elems = append(elems, types.ObjectValueMust(grantEntryType.AttrTypes, map[string]attr.Value{
			"permission": types.StringValue(g.Permission), "scope_path": strOrNull(g.ScopePath),
			"resource_uuid": strOrNull(g.ResourceUUID), "effect": types.StringValue(g.Effect),
		}))
	}
	return types.SetValue(grantEntryType, elems)
}

func membersToSet(ms []member) (types.Set, diag.Diagnostics) {
	elems := make([]attr.Value, 0, len(ms))
	for _, m := range ms {
		elems = append(elems, types.ObjectValueMust(memberType.AttrTypes, map[string]attr.Value{
			"type": types.StringValue(m.Type), "id": types.StringValue(m.ID),
		}))
	}
	return types.SetValue(memberType, elems)
}

// ── reading ───────────────────────────────────────────────────────────────

// principalAccess reads and writes the roles and grants one principal holds
// directly.
type principalAccess struct {
	api                *client.Client
	orgID, ptype, ptID string
}

// grantRow is a permission grant with the id deleting it takes.
type grantRow struct {
	grantEntry
	id string
}

type accessSnapshot struct {
	roles  []roleBinding
	grants []grantRow
}

// read lists the principal's roles and grants. A user's organization-wide
// roles come from its role list, which also holds the roles given at sign-up
// or by invitation; resource_type is matched from prior.
func (a principalAccess) read(ctx context.Context, catalog *permissionCatalog, prior []roleBinding) (accessSnapshot, error) {
	var snap accessSnapshot
	if a.ptype == client.PrincipalUser {
		roles, err := a.api.ListUserRoles(ctx, a.orgID, a.ptID)
		if err != nil {
			return snap, err
		}
		for _, r := range roles {
			snap.roles = append(snap.roles, roleBinding{RoleID: r.Id.String()})
		}
	}
	grants, err := a.api.ListGrants(ctx, a.orgID, a.ptype, a.ptID)
	if err != nil {
		return snap, err
	}
	used := make([]bool, len(prior))
	for _, g := range grants {
		scope := strings.TrimSpace(strOf(optString(g.ScopePath)))
		switch {
		case g.PermissionId != nil:
			snap.grants = append(snap.grants, grantRow{grantEntry{
				Permission: catalog.key(*g.PermissionId), ScopePath: scope, ResourceUUID: uuidOrEmpty(g.ResourceUuid), Effect: g.Effect,
			}, g.Id.String()})
		case g.RoleId != nil:
			if a.ptype == client.PrincipalUser && scope == "" {
				continue // mirrors a row of the user's role list
			}
			b := roleBinding{RoleID: g.RoleId.String(), ScopePath: scope, ResourceUUID: uuidOrEmpty(g.ResourceUuid)}
			for i, p := range prior {
				if !used[i] && p.RoleID == b.RoleID && p.ScopePath == b.ScopePath && p.ResourceUUID == b.ResourceUUID {
					used[i], b.ResourceType = true, p.ResourceType
					break
				}
			}
			snap.roles = append(snap.roles, b)
		}
	}
	return snap, nil
}

func (s accessSnapshot) grantEntries() []grantEntry {
	out := make([]grantEntry, 0, len(s.grants))
	for _, g := range s.grants {
		out = append(out, g.grantEntry)
	}
	return out
}

// ── writing ───────────────────────────────────────────────────────────────

// syncRoles makes the principal's bindings exactly desired. Unbinding removes
// every binding of a role at a scope, so a binding sharing that key with an
// extra one is bound again afterwards.
func (a principalAccess) syncRoles(ctx context.Context, current, desired []roleBinding) error {
	want := map[roleBinding]bool{}
	for _, b := range desired {
		want[b] = true
	}
	have := map[roleBinding]bool{}
	unbind := map[[2]string]bool{}
	for _, b := range current {
		have[b] = true
		if !want[b] {
			unbind[b.key()] = true
		}
	}
	for _, b := range sortedBindings(desired) {
		if !have[b] && !unbind[b.key()] {
			if err := a.api.BindRole(ctx, a.orgID, a.ptype, a.ptID, b.RoleID, b.scope()); err != nil {
				return err
			}
		}
	}
	keys := make([][2]string, 0, len(unbind))
	for k := range unbind {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i][0]+keys[i][1] < keys[j][0]+keys[j][1] })
	for _, k := range keys {
		if err := a.api.UnbindRole(ctx, a.orgID, a.ptype, a.ptID, k[0], client.RoleScope{ScopePath: k[1]}); err != nil {
			return err
		}
	}
	for _, b := range sortedBindings(desired) {
		if unbind[b.key()] {
			if err := a.api.BindRole(ctx, a.orgID, a.ptype, a.ptID, b.RoleID, b.scope()); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncGrants creates the missing grants before deleting the extra ones.
func (a principalAccess) syncGrants(ctx context.Context, catalog *permissionCatalog, current []grantRow, desired []grantEntry) error {
	want := map[grantEntry]bool{}
	for _, g := range desired {
		want[g] = true
	}
	have := map[grantEntry]bool{}
	for _, g := range current {
		have[g.grantEntry] = true
	}
	for _, g := range desired {
		if have[g] {
			continue
		}
		ids, err := catalog.ids([]string{g.Permission})
		if err != nil {
			return err
		}
		effect := console.Effect(g.Effect)
		body := console.CreateGrantRequestBody{PermissionId: ids[0], Effect: &effect}
		if g.ScopePath != "" {
			body.ScopePath = &g.ScopePath
		}
		if g.ResourceUUID != "" {
			u, err := uuidFromString(g.ResourceUUID)
			if err != nil {
				return err
			}
			body.ResourceUuid = &u
		}
		if _, err := a.api.CreateGrant(ctx, a.orgID, a.ptype, a.ptID, body); err != nil {
			return err
		}
	}
	for _, g := range current {
		if !want[g.grantEntry] {
			if err := a.api.DeleteGrant(ctx, a.orgID, g.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedBindings(bs []roleBinding) []roleBinding {
	out := append([]roleBinding{}, bs...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].RoleID+out[i].ScopePath+out[i].ResourceUUID < out[j].RoleID+out[j].ScopePath+out[j].ResourceUUID
	})
	return out
}

// ── plan-time catalog check ───────────────────────────────────────────────

// checkPermissionKeys resolves the permission keys a plan sets against the
// catalog, so an unknown key fails the plan instead of the apply.
func checkPermissionKeys(ctx context.Context, p *providerData, at path.Path, keys []string, diags *diag.Diagnostics) {
	if p == nil || len(keys) == 0 {
		return
	}
	catalog, err := loadPermissionCatalog(ctx, p.API)
	if err != nil {
		diags.AddError("Failed to read the permission catalog", err.Error())
		return
	}
	if _, err := catalog.ids(keys); err != nil {
		diags.AddAttributeError(at, "Unknown permission", err.Error())
	}
}

// plannedGrantKeys returns the permission keys of the known grants in a plan.
func plannedGrantKeys(ctx context.Context, plan tfsdk.Plan) []string {
	var grants types.Set
	if plan.GetAttribute(ctx, path.Root("grants"), &grants).HasError() {
		return nil
	}
	var keys []string
	for _, g := range grantEntriesFromSet(grants) {
		if g.Permission != "" {
			keys = append(keys, g.Permission)
		}
	}
	return keys
}

// modifyAccessPlan checks the grant keys of a plan that is not a destroy.
func modifyAccessPlan(ctx context.Context, p *providerData, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	checkPermissionKeys(ctx, p, path.Root("grants"), plannedGrantKeys(ctx, req.Plan), &resp.Diagnostics)
}

func isGone(err error) bool { return errors.Is(err, client.ErrNotFound) }

func uuidOrEmpty(u *openapi_types.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}

// ── applying ──────────────────────────────────────────────────────────────

// apply writes the managed roles and grants, then reads back what the
// principal holds. After a failed write the read-back is still returned, so
// state records what succeeded and the next plan reconverges.
func (a principalAccess) apply(ctx context.Context, managed map[string]bool, roles, grants types.Set, prior []roleBinding) (snap accessSnapshot, writeErr, readErr error) {
	catalog, err := loadPermissionCatalog(ctx, a.api)
	if err != nil {
		return snap, nil, err
	}
	desired := roleBindingsFromSet(roles)
	if managed["roles"] || managed["grants"] {
		cur, err := a.read(ctx, catalog, prior)
		if err != nil {
			return snap, nil, err
		}
		if managed["roles"] {
			writeErr = a.syncRoles(ctx, cur.roles, desired)
			prior = append(desired, prior...)
		}
		if writeErr == nil && managed["grants"] {
			writeErr = a.syncGrants(ctx, catalog, cur.grants, grantEntriesFromSet(grants))
		}
	}
	snap, readErr = a.read(ctx, catalog, prior)
	return snap, writeErr, readErr
}

// sets converts a snapshot to the roles and grants attributes.
func (s accessSnapshot) sets() (roles, grants types.Set, diags diag.Diagnostics) {
	roles, d := roleBindingsToSet(s.roles)
	diags.Append(d...)
	grants, d = grantEntriesToSet(s.grantEntries())
	diags.Append(d...)
	return roles, grants, diags
}

// knownOrNull is what state records for a planned list when the read-back
// failed: an unknown value cannot be stored.
func knownOrNull(s types.Set) types.Set {
	if s.IsUnknown() {
		return types.SetNull(s.ElementType(context.Background()))
	}
	return s
}

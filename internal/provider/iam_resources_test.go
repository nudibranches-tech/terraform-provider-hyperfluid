// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func strs(ss ...string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func roleEntry(roleID, scope, anchor, family string) map[string]any {
	m := map[string]any{"role_id": roleID}
	if scope != "" {
		m["scope_path"] = scope
	}
	if anchor != "" {
		m["resource_uuid"] = anchor
	}
	if family != "" {
		m["resource_type"] = family
	}
	return m
}

func grantEntryLit(permission, scope, anchor, effect string) map[string]any {
	m := map[string]any{"permission": permission}
	if scope != "" {
		m["scope_path"] = scope
	}
	if anchor != "" {
		m["resource_uuid"] = anchor
	}
	if effect != "" {
		m["effect"] = effect
	}
	return m
}

func (f *fakeIAM) bundleKeys(roleID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, id := range f.roleBundles[roleID] {
		for _, e := range f.catalog {
			if e.Id == id {
				out = append(out, e.Key)
			}
		}
	}
	return out
}

func (f *fakeIAM) setBundle(roleID string, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := []uuid.UUID{}
	for _, k := range keys {
		for _, e := range f.catalog {
			if e.Key == k {
				ids = append(ids, e.Id)
			}
		}
	}
	f.roleBundles[roleID] = ids
}

func (f *fakeIAM) addGrant(ptype, pid, key, scope string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	perm := f.permissionID(key)
	g := console.Grant{Id: uuid.New(), PrincipalType: ptype, PrincipalId: uuid.MustParse(pid), OrganizationId: f.org, PermissionId: &perm, Effect: "allow"}
	if scope != "" {
		g.ScopePath = &scope
	}
	f.grants = append(f.grants, g)
}

func (f *fakeIAM) addMember(groupID, ptype, pid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[groupID] = append(f.members[groupID], console.GroupMember{PrincipalType: ptype, PrincipalId: uuid.MustParse(pid), PrincipalName: "x"})
}

func (f *fakeIAM) count(method, suffix string) int { return len(f.calls(method, suffix)) }

// ── hyperfluid_role ───────────────────────────────────────────────────────

func TestRoleResourcePermissions(t *testing.T) {
	f, pd := newFakeIAM(t)
	d := newDriver(t, pd, "hyperfluid_role")
	cfg := map[string]any{"name": "auditor", "color": "#0f766e", "permissions": strs("secret:read", "secret:list")}

	s := d.mustApply(tfState{}, d.config(cfg))
	id := idOf(t, s)
	if got := f.bundleKeys(id); len(got) != 2 {
		t.Fatalf("bundle = %v", got)
	}

	// Reordering the set plans nothing.
	cfg["permissions"] = strs("secret:list", "secret:read")
	if p := d.plan(s, d.config(cfg)); p.err != nil || !p.noop(s) {
		t.Fatalf("reordered permissions planned a change: %v", p.err)
	}

	// Authoritative: a permission added in the console is removed, a new one added.
	f.setBundle(id, "secret:read", "secret:list", "secret:update")
	s = d.refresh(s)
	cfg["permissions"] = strs("secret:read", "bucket:read_objects")
	s = d.mustApply(s, d.config(cfg))
	wantRows(t, s, "permissions", "bucket:read_objects", "secret:read")

	// Removing the attribute clears the bundle once, then leaves it alone.
	delete(cfg, "permissions")
	s = d.mustApply(s, d.config(cfg))
	wantRows(t, s, "permissions")
	f.setBundle(id, "secret:update")
	s = d.refresh(s)
	puts := f.count("PUT", "/permissions")
	s = d.mustApply(s, d.config(cfg))
	if f.count("PUT", "/permissions") != puts {
		t.Error("an unmanaged bundle was written")
	}
	wantRows(t, s, "permissions", "secret:update")

	if err := d.destroy(s); err != nil {
		t.Fatal(err)
	}
	if f.roles[id] != nil {
		t.Error("destroy must delete the role")
	}
}

func TestRoleResourceOmittedAndImported(t *testing.T) {
	f, pd := newFakeIAM(t)
	d := newDriver(t, pd, "hyperfluid_role")
	cfg := map[string]any{"name": "ops"}
	s := d.mustApply(tfState{}, d.config(cfg))
	id := idOf(t, s)

	f.setBundle(id, "secret:read")
	s = d.refresh(s)
	wantRows(t, s, "permissions", "secret:read")
	if p := d.plan(s, d.config(cfg)); !p.noop(s) {
		t.Fatal("a bundle the configuration never set must not plan a change")
	}

	imported, err := d.importState(id)
	if err != nil {
		t.Fatal(err)
	}
	if p := d.plan(imported, d.config(cfg)); !p.noop(imported) {
		t.Fatal("no list is managed after import")
	}
	cfg["permissions"] = strs("secret:list")
	d.mustApply(imported, d.config(cfg))
	if got := f.bundleKeys(id); len(got) != 1 || got[0] != "secret:list" {
		t.Fatalf("bundle = %v", got)
	}
}

func TestRoleResourceValidation(t *testing.T) {
	f, pd := newFakeIAM(t)
	owner := f.addRole("owner", true)
	d := newDriver(t, pd, "hyperfluid_role")

	wantErr(t, d.plan(tfState{}, d.config(map[string]any{"name": "Owner"})).err, "Reserved role name")
	wantErr(t, d.plan(tfState{}, d.config(map[string]any{"name": "x", "permissions": strs("secret:frobnicate")})).err,
		`unknown permission "secret:frobnicate"`)
	_, err := d.importState(owner.Id.String())
	wantErr(t, err, "System role cannot be imported")
}

// ── hyperfluid_group ──────────────────────────────────────────────────────

func TestGroupResourceAuthoritativeLists(t *testing.T) {
	f, pd := newFakeIAM(t)
	auditor := f.addRole("auditor", false).Id.String()
	alice, bob, anchor := uuid.NewString(), uuid.NewString(), uuid.NewString()
	sa := f.addAccount("etl", "etl").row.Id.String()
	d := newDriver(t, pd, "hyperfluid_group")

	cfg := map[string]any{
		"name": "data-engineering",
		"members": []any{
			map[string]any{"type": "user", "id": alice},
			map[string]any{"type": "service_account", "id": sa},
		},
		"roles": []any{
			roleEntry(auditor, "", "", ""),
			roleEntry(auditor, "analytics/reports", anchor, "secret"),
		},
		"grants": []any{
			grantEntryLit("secret:read", "prod/*", "", ""),
			grantEntryLit("secret:update", "prod/*", "", "deny"),
		},
	}
	s := d.mustApply(tfState{}, d.config(cfg))
	id := idOf(t, s)
	wantRows(t, s, "members", alice+"|user", sa+"|service_account")
	wantRows(t, s, "roles", "||"+auditor+"|", "secret|"+anchor+"|"+auditor+"|analytics/reports")
	wantRows(t, s, "grants", "allow|secret:read||prod/*", "deny|secret:update||prod/*")

	reordered := map[string]any{"name": cfg["name"]}
	for _, name := range []string{"members", "roles", "grants"} {
		l, _ := cfg[name].([]any)
		reordered[name] = []any{l[1], l[0]}
	}
	if p := d.plan(s, d.config(reordered)); p.err != nil || !p.noop(s) {
		t.Fatalf("reordered lists planned a change: %v", p.err)
	}

	// Changes made in the console are reverted, and the configuration's own
	// changes applied.
	f.addMember(id, "user", bob)
	f.addGrant("group", id, "bucket:list_objects", "")
	s = d.refresh(s)
	cfg["members"] = []any{map[string]any{"type": "user", "id": alice}}
	cfg["grants"] = []any{grantEntryLit("secret:read", "prod/*", "", "allow")}
	s = d.mustApply(s, d.config(cfg))
	wantRows(t, s, "members", alice+"|user")
	wantRows(t, s, "grants", "allow|secret:read||prod/*")
	wantRows(t, s, "roles", "||"+auditor+"|", "secret|"+anchor+"|"+auditor+"|analytics/reports")

	// Removing a list clears it.
	delete(cfg, "roles")
	s = d.mustApply(s, d.config(cfg))
	wantRows(t, s, "roles")

	if err := d.destroy(s); err != nil {
		t.Fatal(err)
	}
	if f.groups[id] != nil || len(f.grants) != 0 {
		t.Errorf("destroy must delete the group and its grants: %d grants left", len(f.grants))
	}
}

func TestGroupResourceOmittedListsAreUntouched(t *testing.T) {
	f, pd := newFakeIAM(t)
	d := newDriver(t, pd, "hyperfluid_group")
	cfg := map[string]any{"name": "readers"}
	s := d.mustApply(tfState{}, d.config(cfg))
	id := idOf(t, s)

	alice := uuid.NewString()
	f.addMember(id, "user", alice)
	f.addGrant("group", id, "secret:read", "")
	s = d.refresh(s)
	s = d.mustApply(s, d.config(map[string]any{"name": "readers", "description": "renamed"}))
	wantRows(t, s, "members", alice+"|user")
	wantRows(t, s, "grants", "allow|secret:read||")
	if f.count("DELETE", "/members/user/"+alice) != 0 || f.count("POST", "/grants") != 0 {
		t.Error("an omitted list was written")
	}

	imported, err := d.importState(id)
	if err != nil {
		t.Fatal(err)
	}
	if p := d.plan(imported, d.config(map[string]any{"name": "readers", "description": "renamed"})); !p.noop(imported) {
		t.Fatalf("no list is managed after import: %v", p.planned)
	}
}

func TestGroupResourcePartialFailureReconverges(t *testing.T) {
	f, pd := newFakeIAM(t)
	d := newDriver(t, pd, "hyperfluid_group")
	alice, bob := uuid.NewString(), uuid.NewString()
	cfg := map[string]any{"name": "g", "members": []any{map[string]any{"type": "user", "id": alice}}}
	s := d.mustApply(tfState{}, d.config(cfg))
	id := idOf(t, s)

	// Adding bob succeeds, removing alice is refused.
	f.forbid["DELETE /api/v1/organizations/"+f.org.String()+"/groups/"+id+"/members"] = "You are not authorized"
	cfg["members"] = []any{map[string]any{"type": "user", "id": bob}}
	s, err := d.apply(s, d.config(cfg))
	wantErr(t, err, "You are not authorized")
	wantRows(t, s, "members", alice+"|user", bob+"|user")

	delete(f.forbid, "DELETE /api/v1/organizations/"+f.org.String()+"/groups/"+id+"/members")
	p := d.plan(s, d.config(cfg))
	if p.err != nil || p.noop(s) {
		t.Fatal("the next plan must reconverge")
	}
	s = d.mustApply(s, d.config(cfg))
	wantRows(t, s, "members", bob+"|user")
}

func TestScopeValidation(t *testing.T) {
	f, pd := newFakeIAM(t)
	role := f.addRole("r", false).Id.String()
	anchor := uuid.NewString()
	d := newDriver(t, pd, "hyperfluid_group")
	plan := func(entry string, v map[string]any) error {
		return d.plan(tfState{}, d.config(map[string]any{"name": "g", entry: []any{v}})).err
	}
	wantErr(t, plan("roles", roleEntry(role, "prod/*", anchor, "")), "Exact scope required")
	wantErr(t, plan("roles", roleEntry(role, "", "", "secret")), "Exact scope required")
	wantErr(t, plan("grants", grantEntryLit("secret:read", "", anchor, "")), "Exact scope required")
	wantErr(t, plan("grants", grantEntryLit("secret:nope", "", "", "")), `unknown permission "secret:nope"`)
	if err := plan("roles", roleEntry(role, "prod/db", anchor, "secret")); err != nil {
		t.Fatalf("an exact scope must be accepted: %v", err)
	}
}

// ── hyperfluid_service_account_access ─────────────────────────────────────

func TestServiceAccountAccess(t *testing.T) {
	f, pd := newFakeIAM(t)
	role := f.addRole("reader", false).Id.String()
	acct := f.addAccount("etl", "etl")
	sa := acct.row.Id.String()
	acct.attributes = []string{"team::data"}
	d := newDriver(t, pd, "hyperfluid_service_account_access")

	cfg := map[string]any{
		"service_account_id": sa,
		"roles":              []any{roleEntry(role, "analytics", "", "")},
		"grants":             []any{grantEntryLit("secret:read", "prod/db", "", "")},
	}
	s := d.mustApply(tfState{}, d.config(cfg))
	wantRows(t, s, "roles", "||"+role+"|analytics")
	wantRows(t, s, "attributes", "team::data")

	cfg["attributes"] = strs("region::eu")
	s = d.mustApply(s, d.config(cfg))
	wantRows(t, s, "attributes", "region::eu")

	// Destroy clears the managed lists only: the account and the grant added in
	// the console after attributes stopped being managed stay.
	delete(cfg, "attributes")
	s = d.mustApply(s, d.config(cfg))
	acct.attributes = []string{"team::ops"}
	s = d.refresh(s)
	if err := d.destroy(s); err != nil {
		t.Fatal(err)
	}
	if len(f.grants) != 0 {
		t.Errorf("managed roles and grants must be cleared, %d grants left", len(f.grants))
	}
	if f.accountByID(sa) == nil || len(acct.attributes) != 1 {
		t.Errorf("destroy must keep the account and its unmanaged attributes: %v", acct.attributes)
	}

	imported, err := d.importState(sa)
	if err != nil {
		t.Fatal(err)
	}
	if p := d.plan(imported, d.config(map[string]any{"service_account_id": sa})); !p.noop(imported) {
		t.Fatal("no list is managed after import")
	}
	if err := d.destroy(imported); err != nil {
		t.Fatal(err)
	}
	if len(acct.attributes) != 1 {
		t.Error("destroying an imported resource must not touch anything")
	}
}

// ── hyperfluid_user_access ────────────────────────────────────────────────

func TestUserAccessOrgWideRoles(t *testing.T) {
	f, pd := newFakeIAM(t)
	owner := f.addRole("owner", true).Id.String()
	viewer := f.addRole("viewer", true).Id.String()
	editor := f.addRole("editor", true).Id.String()
	alice := f.addUser(owner)
	bob := f.addUser(viewer)
	d := newDriver(t, pd, "hyperfluid_user_access")

	// A role given at sign-up reads back.
	imported, err := d.importState(bob)
	if err != nil {
		t.Fatal(err)
	}
	wantRows(t, imported, "roles", "||"+viewer+"|")

	cfg := map[string]any{"user_id": bob, "roles": []any{roleEntry(viewer, "", "", ""), roleEntry(editor, "prod", "", "")}}
	s := d.mustApply(imported, d.config(cfg))
	wantRows(t, s, "roles", "||"+viewer+"|", "||"+editor+"|prod")
	if f.count("POST", "/roles/"+viewer) != 0 {
		t.Error("a role the user already holds was bound again")
	}

	// Removing the last owner is refused with the server's message.
	ds := newDriver(t, pd, "hyperfluid_user_access")
	sa, err := ds.importState(alice)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ds.apply(sa, ds.config(map[string]any{"user_id": alice, "roles": []any{}}))
	wantErr(t, err, "Cannot remove the organization's last owner")

	// Destroy unbinds the managed roles, the user stays.
	if err := d.destroy(s); err != nil {
		t.Fatal(err)
	}
	if len(f.userRoles[bob]) != 0 || len(f.grants) != 0 {
		t.Errorf("roles left: %v, grants left: %d", f.userRoles[bob], len(f.grants))
	}
}

func TestGroupDataSourceMembers(t *testing.T) {
	f, pd := newFakeIAM(t)
	g := &console.Group{Id: uuid.New(), OrganizationId: f.org, Name: "readers"}
	f.groups[g.Id.String()] = g
	alice := uuid.NewString()
	f.addMember(g.Id.String(), "user", alice)
	got, d := readDataSource(t, &groupDataSource{p: pd}, groupDataSourceModel{
		Name: types.StringValue("readers"), ID: types.StringNull(), Description: types.StringNull(),
		MemberCount: types.Int64Null(), Members: types.SetNull(memberType),
	})
	mustSucceed(t, d)
	if ms := membersFromSet(got.Members); len(ms) != 1 || ms[0] != (member{Type: "user", ID: alice}) {
		t.Fatalf("members = %v", ms)
	}
}

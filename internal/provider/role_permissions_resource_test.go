// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func keySet(t *testing.T, keys ...string) types.Set {
	t.Helper()
	s, d := stringSliceToSet(t.Context(), append([]string{}, keys...))
	mustSucceed(t, d)
	return s
}

func setKeys(t *testing.T, s types.Set) []string {
	t.Helper()
	out, d := setToStringSlice(t.Context(), s)
	mustSucceed(t, d)
	sort.Strings(out)
	return out
}

func TestRolePermissionsIsAuthoritative(t *testing.T) {
	f, pd := newFakeIAM(t)
	role := uuid.NewString()
	// Held before Terraform manages the role; apply replaces it.
	f.roleBundles[role] = []uuid.UUID{f.permissionID("secret:update")}
	l := newLifecycle[rolePermissionsModel](t, &rolePermissionsResource{p: pd})

	plan := rolePermissionsModel{ID: types.StringUnknown(), RoleID: types.StringValue(role), Permissions: keySet(t, "secret:read", "secret:list")}
	created, d := l.create(plan)
	mustSucceed(t, d)
	if got := setKeys(t, created.Permissions); len(got) != 2 || got[0] != "secret:list" || got[1] != "secret:read" {
		t.Fatalf("permissions = %v", got)
	}
	if created.ID.ValueString() != role {
		t.Errorf("id = %v", created.ID)
	}
	puts := f.calls("PUT", "/roles/"+role+"/permissions")
	if ids, _ := puts[0].body["permission_ids"].([]any); len(puts) != 1 || len(ids) != 2 {
		t.Fatalf("PUT = %+v", puts)
	}

	// A permission added out of band shows up as drift.
	f.roleBundles[role] = append(f.roleBundles[role], f.permissionID("bucket:read_objects"))
	read, _, d := l.read(created)
	mustSucceed(t, d)
	if got := setKeys(t, read.Permissions); len(got) != 3 {
		t.Errorf("drift not reported: %v", got)
	}

	next := plan
	next.Permissions = keySet(t, "secret:read")
	updated, d := l.update(next, read)
	mustSucceed(t, d)
	if got := setKeys(t, updated.Permissions); len(got) != 1 || got[0] != "secret:read" {
		t.Errorf("updated = %v", got)
	}

	imported, d := l.importAndRead(role)
	mustSucceed(t, d)
	if !imported.Permissions.Equal(updated.Permissions) || imported.RoleID != updated.RoleID {
		t.Errorf("imported = %+v, want %+v", imported, updated)
	}

	// Destroy empties the bundle.
	mustSucceed(t, l.destroy(updated))
	if n := len(f.roleBundles[role]); n != 0 {
		t.Errorf("bundle after destroy holds %d permissions", n)
	}
	last := f.calls("PUT", "/roles/"+role+"/permissions")
	if ids, ok := last[len(last)-1].body["permission_ids"].([]any); !ok || len(ids) != 0 {
		t.Errorf("destroy must send an empty list, not omit it: %v", last[len(last)-1].body)
	}
}

func TestRolePermissionsRefusals(t *testing.T) {
	f, pd := newFakeIAM(t)
	l := newLifecycle[rolePermissionsModel](t, &rolePermissionsResource{p: pd})

	system := uuid.NewString()
	f.systemRoles[system] = true
	_, d := l.create(rolePermissionsModel{ID: types.StringUnknown(), RoleID: types.StringValue(system), Permissions: keySet(t, "secret:read")})
	mustFail(t, d, "System role permission bundles")

	custom := uuid.NewString()
	f.roleBundles[custom] = []uuid.UUID{}
	_, d = l.create(rolePermissionsModel{ID: types.StringUnknown(), RoleID: types.StringValue(custom), Permissions: keySet(t, "secret:read", "secret:fly")})
	mustFail(t, d, `unknown permission "secret:fly"`)

	// A role deleted out of band leaves state.
	gone := rolePermissionsModel{ID: types.StringValue(uuid.NewString()), RoleID: types.StringValue(uuid.NewString()), Permissions: keySet(t)}
	if _, removed, d := l.read(gone); !removed || d.HasError() {
		t.Errorf("removed=%v, %v", removed, d)
	}
}

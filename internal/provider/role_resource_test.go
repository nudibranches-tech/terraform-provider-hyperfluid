// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func rolePlan(name string) roleModel {
	return roleModel{ID: types.StringUnknown(), Name: types.StringValue(name), Description: types.StringNull(), Color: types.StringNull()}
}

func TestRoleLifecycle(t *testing.T) {
	f, pd := newFakeIAM(t)
	l := newLifecycle[roleModel](t, &roleResource{p: pd})

	plan := rolePlan("auditor")
	plan.Description = types.StringValue("reads everything")
	plan.Color = types.StringValue("#0f766e")
	created, d := l.create(plan)
	mustSucceed(t, d)
	if created.ID.IsUnknown() || created.Description.ValueString() != "reads everything" || created.Color.ValueString() != "#0f766e" {
		t.Fatalf("created = %+v", created)
	}
	if body := f.calls("POST", "/api/v1/roles")[0].body; body["organization_id"] != f.org.String() {
		t.Errorf("create body = %v, want the provider's organization", body)
	}

	// Renamed in place; the color removed from the configuration is cleared.
	next := plan
	next.ID, next.Name, next.Color = created.ID, types.StringValue("auditors"), types.StringNull()
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if updated.ID != created.ID || updated.Name.ValueString() != "auditors" || !updated.Color.IsNull() {
		t.Fatalf("updated = %+v", updated)
	}

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if imported != updated {
		t.Errorf("imported = %+v, want %+v", imported, updated)
	}

	mustSucceed(t, l.destroy(updated))
	if _, removed, d := l.read(updated); !removed || d.HasError() {
		t.Errorf("a deleted role must leave state (removed=%v, %v)", removed, d)
	}
	mustSucceed(t, l.destroy(updated))
}

func TestRoleSystemRolesAreRefused(t *testing.T) {
	f, pd := newFakeIAM(t)
	viewer := f.addRole("viewer", true)
	l := newLifecycle[roleModel](t, &roleResource{p: pd})

	_, d := l.importAndRead(viewer.Id.String())
	mustFail(t, d, "System role cannot be imported")

	s := resourceSchema(t, &roleResource{}).Schema
	for name, wantErr := range map[string]bool{"viewer": true, " Owner ": true, "EDITOR": true, "viewers": false, "auditor": false} {
		d := validateString(t, s, nullConfig(t, s, nil), path.Root("name"), types.StringValue(name))
		if d.HasError() != wantErr {
			t.Errorf("name %q: error = %v, want %v", name, d, wantErr)
		}
	}
}

func TestRoleDataSourceFindsSystemRoles(t *testing.T) {
	f, pd := newFakeIAM(t)
	viewer := f.addRole("viewer", true)
	f.addRole("viewer-plus", false)
	f.roleBundles[viewer.Id.String()] = []uuid.UUID{f.permissionID("secret:list"), f.permissionID("secret:read")}
	ds := &roleDataSource{p: pd}

	got, d := readDataSource(t, ds, roleDataSourceModel{Name: types.StringValue("viewer"), Permissions: types.SetNull(types.StringType)})
	mustSucceed(t, d)
	if got.ID.ValueString() != viewer.Id.String() || !got.IsSystem.ValueBool() || len(setKeys(t, got.Permissions)) != 2 {
		t.Errorf("viewer = %+v", got)
	}
	got, d = readDataSource(t, ds, roleDataSourceModel{Name: types.StringValue("Viewer"), Permissions: types.SetNull(types.StringType)})
	mustSucceed(t, d)
	if got.ID.ValueString() != viewer.Id.String() {
		t.Errorf("a case-insensitive match must resolve the system role: %+v", got)
	}
	_, d = readDataSource(t, ds, roleDataSourceModel{Name: types.StringValue("nobody"), Permissions: types.SetNull(types.StringType)})
	mustFail(t, d, "not found")
}

func TestUserRoleLifecycle(t *testing.T) {
	f, pd := newFakeIAM(t)
	editor := f.addRole("editor", true)
	user := uuid.NewString()
	l := newLifecycle[userRoleModel](t, &roleBindingResource{p: pd, kind: userRoleKind})

	orgWide := userRoleModel{UserID: types.StringValue(user), roleBindingFields: bindingFields(editor.Id.String(), types.StringNull())}
	scoped := userRoleModel{UserID: types.StringValue(user), roleBindingFields: bindingFields(editor.Id.String(), types.StringValue("prod"))}

	a, d := l.create(orgWide)
	mustSucceed(t, d)
	b, d := l.create(scoped)
	mustSucceed(t, d)
	if a.ID.ValueString() != user+"/"+editor.Id.String() || b.ID.ValueString() != user+"/"+editor.Id.String()+"/prod" {
		t.Fatalf("ids = %v / %v", a.ID, b.ID)
	}
	if len(f.userRoles[user]) != 1 {
		t.Errorf("an org-wide binding must be one of the user's roles: %v", f.userRoles[user])
	}

	got, d := l.importAndRead(b.ID.ValueString())
	mustSucceed(t, d)
	if got != b {
		t.Errorf("imported scoped = %+v, want %+v", got, b)
	}

	mustSucceed(t, l.destroy(a))
	if _, removed, _ := l.read(a); !removed {
		t.Error("the unbound org-wide role must leave state")
	}
	if _, removed, _ := l.read(b); removed {
		t.Error("the scoped binding must survive the org-wide one's removal")
	}
}

// A role assigned at sign-up or by invitation is one of the user's roles with no
// mirror grant; it still reads back, and imports.
func TestUserRoleAssignedOutsideTheGrants(t *testing.T) {
	f, pd := newFakeIAM(t)
	owner := f.addRole("owner", true)
	user := uuid.NewString()
	f.userRoles[user] = []string{owner.Id.String()}
	l := newLifecycle[userRoleModel](t, &roleBindingResource{p: pd, kind: userRoleKind})

	got, d := l.importAndRead(user + "/" + owner.Id.String())
	mustSucceed(t, d)
	if got.ID.ValueString() != user+"/"+owner.Id.String() || got.RoleID.ValueString() != owner.Id.String() || !got.ScopePath.IsNull() {
		t.Errorf("imported = %+v", got)
	}
}

// A user deleted outside Terraform is reported as a 404 by their role list:
// the binding is gone with them.
func TestUserRoleOfADeletedUserLeavesState(t *testing.T) {
	f, pd := newFakeIAM(t)
	viewer := f.addRole("viewer", true)
	user := uuid.NewString()
	l := newLifecycle[userRoleModel](t, &roleBindingResource{p: pd, kind: userRoleKind})

	created, d := l.create(userRoleModel{UserID: types.StringValue(user), roleBindingFields: bindingFields(viewer.Id.String(), types.StringNull())})
	mustSucceed(t, d)
	delete(f.userRoles, user)
	if _, removed, d := l.read(created); !removed || d.HasError() {
		t.Errorf("removed=%v, %v", removed, d)
	}
	// Destroying it then is not an error either.
	mustSucceed(t, l.destroy(created))
}

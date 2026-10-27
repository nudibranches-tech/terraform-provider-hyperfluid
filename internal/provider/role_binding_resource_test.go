// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/google/uuid"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func bindingFields(role string, scope types.String) roleBindingFields {
	return roleBindingFields{
		ID: types.StringUnknown(), RoleID: types.StringValue(role), ScopePath: scope,
		ResourceUUID: types.StringNull(), ResourceType: types.StringNull(),
	}
}

func TestGroupRoleLifecycle(t *testing.T) {
	f, pd := newFakeIAM(t)
	group := console.Group{Id: uuid.New(), Name: "ops"}
	f.groups[group.Id.String()] = &group
	role := uuid.NewString()
	l := newLifecycle[groupRoleModel](t, &roleBindingResource{p: pd, kind: groupRoleKind})

	orgWide := groupRoleModel{GroupID: types.StringValue(group.Id.String()), roleBindingFields: bindingFields(role, types.StringNull())}
	scoped := groupRoleModel{GroupID: types.StringValue(group.Id.String()), roleBindingFields: bindingFields(role, types.StringValue("prod/*"))}

	a, d := l.create(orgWide)
	mustSucceed(t, d)
	b, d := l.create(scoped)
	mustSucceed(t, d)
	if a.ID.IsUnknown() || b.ID.IsUnknown() || a.ID == b.ID {
		t.Fatalf("the two bindings must be distinct grants: %v / %v", a.ID, b.ID)
	}
	if q := f.calls("POST", "/roles/"+role)[1].query; q != "scope_path=prod%2F%2A" {
		t.Errorf("scoped bind query = %q", q)
	}

	// Import: organization-wide, then scoped (the scope path keeps its slash).
	got, d := l.importAndRead(group.Id.String() + "/" + role)
	mustSucceed(t, d)
	if got != a {
		t.Errorf("imported org-wide = %+v, want %+v", got, a)
	}
	got, d = l.importAndRead(group.Id.String() + "/" + role + "/prod/*")
	mustSucceed(t, d)
	if got != b {
		t.Errorf("imported scoped = %+v, want %+v", got, b)
	}

	// Unbinding the scoped one leaves the org-wide one in place.
	mustSucceed(t, l.destroy(b))
	if _, removed, _ := l.read(b); !removed {
		t.Error("the unbound binding must leave state")
	}
	if _, removed, _ := l.read(a); removed {
		t.Error("the org-wide binding must survive the scoped one's removal")
	}
	if q := f.calls("DELETE", "/roles/"+role)[0].query; q != "scope_path=prod%2F%2A" {
		t.Errorf("unbind query = %q", q)
	}
}

func TestServiceAccountRoleBindsByAccountID(t *testing.T) {
	f, pd := newFakeIAM(t)
	acct := f.addAccount("ci-deployer", "CI deployer")
	role := uuid.NewString()
	l := newLifecycle[serviceAccountRoleModel](t, &roleBindingResource{p: pd, kind: serviceAccountRoleKind})

	plan := serviceAccountRoleModel{ServiceAccountID: types.StringValue(acct.row.Id.String()), roleBindingFields: bindingFields(role, types.StringNull())}
	created, d := l.create(plan)
	mustSucceed(t, d)
	if len(f.calls("POST", "/service-accounts/"+acct.row.Id.String()+"/roles/"+role)) != 1 || created.ID.IsUnknown() {
		t.Fatalf("created = %+v", created)
	}
	// Removed out of band: dropped from state.
	f.grants = nil
	if _, removed, d := l.read(created); !removed || d.HasError() {
		t.Errorf("removed=%v, %v", removed, d)
	}
}

// An anchored binding names one concrete resource: a wildcard or missing
// scope_path is refused at plan time, as the API would.
func TestRoleBindingAnchorNeedsAnExactScope(t *testing.T) {
	r := &roleBindingResource{kind: groupRoleKind}
	s := resourceSchema(t, r).Schema
	anchor := tftypes.NewValue(tftypes.String, uuid.NewString())
	for name, tc := range map[string]struct {
		scope   tftypes.Value
		wantErr bool
	}{
		"exact":    {tftypes.NewValue(tftypes.String, "prod/vault"), false},
		"wildcard": {tftypes.NewValue(tftypes.String, "prod/*"), true},
		"org-wide": {tftypes.NewValue(tftypes.String, nil), true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := nullConfig(t, s, map[string]tftypes.Value{"scope_path": tc.scope, "resource_uuid": anchor})
			resp := &fwresource.ValidateConfigResponse{}
			r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: cfg}, resp)
			if resp.Diagnostics.HasError() != tc.wantErr {
				t.Errorf("error = %v, want %v", resp.Diagnostics, tc.wantErr)
			}
		})
	}
}

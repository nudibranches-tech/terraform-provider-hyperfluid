// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func grantPlan(ptype, pid, permission string, scope types.String) grantModel {
	return grantModel{
		ID: types.StringUnknown(), PrincipalType: types.StringValue(ptype), PrincipalID: types.StringValue(pid),
		Permission: types.StringValue(permission), PermissionID: types.StringUnknown(), ScopePath: scope,
		ResourceUUID: types.StringNull(), Effect: types.StringValue("allow"),
	}
}

func TestGrantLifecycle(t *testing.T) {
	f, pd := newFakeIAM(t)
	group := console.Group{Id: uuid.New(), Name: "ops"}
	f.groups[group.Id.String()] = &group
	l := newLifecycle[grantModel](t, &grantResource{p: pd})

	plan := grantPlan("group", group.Id.String(), "secret:read", types.StringValue("prod/*"))
	created, d := l.create(plan)
	mustSucceed(t, d)
	if created.ID.IsUnknown() || created.Permission.ValueString() != "secret:read" ||
		created.PermissionID.ValueString() != f.permissionID("secret:read").String() ||
		created.ScopePath.ValueString() != "prod/*" || created.Effect.ValueString() != "allow" {
		t.Fatalf("created = %+v", created)
	}
	body := f.calls("POST", "/groups/"+group.Id.String()+"/grants")[0].body
	if body["permission_id"] != f.permissionID("secret:read").String() || body["scope_path"] != "prod/*" || body["effect"] != "allow" {
		t.Errorf("create body = %v", body)
	}

	read, removed, d := l.read(created)
	mustSucceed(t, d)
	if removed || read != created {
		t.Errorf("read = %+v (removed %v), want %+v", read, removed, created)
	}

	imported, d := l.importAndRead("group/" + group.Id.String() + "/" + created.ID.ValueString())
	mustSucceed(t, d)
	if imported != created {
		t.Errorf("imported = %+v, want %+v", imported, created)
	}

	mustSucceed(t, l.destroy(created))
	if _, removed, d := l.read(created); !removed || d.HasError() {
		t.Errorf("a deleted grant must leave state (removed=%v, %v)", removed, d)
	}
	// Destroying a grant already gone succeeds.
	mustSucceed(t, l.destroy(created))
}

func TestGrantForEachPrincipalType(t *testing.T) {
	f, pd := newFakeIAM(t)
	acct := f.addAccount("etl", "ETL")
	l := newLifecycle[grantModel](t, &grantResource{p: pd})

	deny := grantPlan("service_account", acct.row.Id.String(), "bucket:read_objects", types.StringNull())
	deny.Effect = types.StringValue("deny")
	got, d := l.create(deny)
	mustSucceed(t, d)
	if got.Effect.ValueString() != "deny" || !got.ScopePath.IsNull() {
		t.Errorf("service account grant = %+v", got)
	}
	if body := f.calls("POST", "/service-accounts/"+acct.row.Id.String()+"/grants")[0].body; body["scope_path"] != nil {
		t.Errorf("an org-wide grant must send no scope_path: %v", body)
	}

	user := uuid.NewString()
	got, d = l.create(grantPlan("user", user, "secret:list", types.StringNull()))
	mustSucceed(t, d)
	if got.PrincipalType.ValueString() != "user" || len(f.calls("POST", "/users/"+user+"/grants")) != 1 {
		t.Errorf("user grant = %+v", got)
	}
}

func TestGrantUnknownPermissionIsNamed(t *testing.T) {
	f, pd := newFakeIAM(t)
	acct := f.addAccount("etl", "ETL")
	l := newLifecycle[grantModel](t, &grantResource{p: pd})
	_, d := l.create(grantPlan("service_account", acct.row.Id.String(), "secret:launch", types.StringNull()))
	mustFail(t, d, `unknown permission "secret:launch"`)
	if n := len(f.calls("POST", "/grants")); n != 0 {
		t.Errorf("no grant may be created for an unknown permission (%d calls)", n)
	}
}

func TestGrantImportIDIsValidated(t *testing.T) {
	_, pd := newFakeIAM(t)
	l := newLifecycle[grantModel](t, &grantResource{p: pd})
	_, d := l.importAndRead(uuid.NewString())
	mustFail(t, d, "<principal_type>/<principal_id>/<grant_id>")
}

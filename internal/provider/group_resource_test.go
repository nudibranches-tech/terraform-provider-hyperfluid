// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func groupPlan(name string, description *string) groupModel {
	return groupModel{
		ID: types.StringUnknown(), Name: types.StringValue(name),
		Description: optString(description), MemberCount: types.Int64Unknown(),
	}
}

func TestGroupLifecycle(t *testing.T) {
	f, pd := newFakeIAM(t)
	l := newLifecycle[groupModel](t, &groupResource{p: pd})

	desc := "data engineers"
	created, d := l.create(groupPlan("data-eng", &desc))
	mustSucceed(t, d)
	if created.ID.IsUnknown() || created.Name.ValueString() != "data-eng" || created.Description.ValueString() != desc ||
		created.MemberCount.ValueInt64() != 0 {
		t.Fatalf("created = %+v", created)
	}

	// Renamed in place, and a description removed from the configuration is cleared.
	next := groupPlan("data-engineering", nil)
	next.ID = created.ID
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if updated.ID != created.ID || updated.Name.ValueString() != "data-engineering" || !updated.Description.IsNull() {
		t.Fatalf("updated = %+v", updated)
	}
	puts := f.calls("PUT", "/groups/"+created.ID.ValueString())
	if len(puts) != 1 || puts[0].body["description"] != nil {
		t.Fatalf("PUT = %+v, want one without a description", puts)
	}

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if imported != updated {
		t.Errorf("imported = %+v, want %+v", imported, updated)
	}

	mustSucceed(t, l.destroy(updated))
	if _, removed, d := l.read(updated); !removed || d.HasError() {
		t.Errorf("a deleted group must leave state without an error (removed=%v, %v)", removed, d)
	}
	// Destroying a group that is already gone succeeds.
	mustSucceed(t, l.destroy(updated))
}

func TestGroupCreateRefusalNamesThePermission(t *testing.T) {
	f, pd := newFakeIAM(t)
	f.forbid["POST /api/v1/organizations/"] = "You are not authorized to manage authorization in this organization"
	l := newLifecycle[groupModel](t, &groupResource{p: pd})

	_, d := l.create(groupPlan("ops", nil))
	mustFail(t, d, `this call requires the "group:create" permission`)
}

func TestGroupDataSourceFindsByName(t *testing.T) {
	_, pd := newFakeIAM(t)
	l := newLifecycle[groupModel](t, &groupResource{p: pd})
	created, d := l.create(groupPlan("auditors", nil))
	mustSucceed(t, d)

	ds := &groupDataSource{p: pd}
	got, d := readDataSource(t, ds, groupModel{Name: types.StringValue("auditors")})
	mustSucceed(t, d)
	if got != created {
		t.Errorf("data source = %+v, want %+v", got, created)
	}
	_, d = readDataSource(t, ds, groupModel{Name: types.StringValue("nobody")})
	mustFail(t, d, "not found")
}

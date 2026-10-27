// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestServiceAccountAttributesLifecycle(t *testing.T) {
	f, pd := newFakeIAM(t)
	acct := f.addAccount("analyst", "Analyst")
	acct.attributes = []string{"team::legacy"}
	l := newLifecycle[serviceAccountAttributesModel](t, &serviceAccountAttributesResource{p: pd})

	plan := serviceAccountAttributesModel{
		ID: types.StringUnknown(), ServiceAccountID: types.StringValue(acct.row.Id.String()),
		Attributes: keySet(t, "region::occitanie", "department::sales"),
	}
	created, d := l.create(plan)
	mustSucceed(t, d)
	if got := setKeys(t, created.Attributes); len(got) != 2 || got[0] != "department::sales" {
		t.Fatalf("attributes = %v", got)
	}

	imported, d := l.importAndRead(acct.row.Id.String())
	mustSucceed(t, d)
	if !imported.Attributes.Equal(created.Attributes) {
		t.Errorf("imported = %+v", imported)
	}

	mustSucceed(t, l.destroy(created))
	if len(acct.attributes) != 0 {
		t.Errorf("destroy must clear the attributes, got %v", acct.attributes)
	}
	delete(f.accounts, "analyst")
	if _, removed, d := l.read(created); !removed || d.HasError() {
		t.Errorf("removed=%v, %v", removed, d)
	}
}

func TestServiceAccountDataSource(t *testing.T) {
	f, pd := newFakeIAM(t)
	acct := f.addAccount("ci-deployer", "CI deployer")
	f.addAccount("ci-deployer-2", "CI deployer 2")
	ds := &serviceAccountDataSource{p: pd}

	byClient, d := readDataSource(t, ds, serviceAccountDataSourceModel{ClientID: types.StringValue("ci-deployer")})
	mustSucceed(t, d)
	if byClient.ID.ValueString() != acct.row.Id.String() || byClient.Name.ValueString() != "CI deployer" || byClient.PlatformManaged.ValueBool() {
		t.Errorf("by client_id = %+v", byClient)
	}
	// "CI deployer" is a substring of both names; only the exact one matches.
	byName, d := readDataSource(t, ds, serviceAccountDataSourceModel{Name: types.StringValue("CI deployer")})
	mustSucceed(t, d)
	if byName.ClientID.ValueString() != "ci-deployer" {
		t.Errorf("by name = %+v", byName)
	}
	_, d = readDataSource(t, ds, serviceAccountDataSourceModel{Name: types.StringValue("nobody")})
	mustFail(t, d, "not found")
}

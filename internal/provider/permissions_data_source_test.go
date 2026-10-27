// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func permissionsConfig(resourceType types.String) permissionsDataSourceModel {
	return permissionsDataSourceModel{
		ResourceType: resourceType,
		Keys:         types.ListNull(types.StringType),
		IDs:          types.MapNull(types.StringType),
		Permissions:  types.ListNull(permissionEntryType()),
	}
}

func TestPermissionsDataSource(t *testing.T) {
	f, pd := newFakeIAM(t)
	ds := &permissionsDataSource{p: pd}

	all, d := readDataSource(t, ds, permissionsConfig(types.StringNull()))
	mustSucceed(t, d)
	if len(all.Keys.Elements()) != len(f.catalog) || len(all.Permissions.Elements()) != len(f.catalog) {
		t.Fatalf("keys = %v", all.Keys)
	}
	ids := map[string]string{}
	mustSucceed(t, all.IDs.ElementsAs(t.Context(), &ids, false))
	if ids["secret:read"] != f.permissionID("secret:read").String() {
		t.Errorf("ids = %v", ids)
	}

	secrets, d := readDataSource(t, ds, permissionsConfig(types.StringValue("secret")))
	mustSucceed(t, d)
	var keys []string
	mustSucceed(t, secrets.Keys.ElementsAs(t.Context(), &keys, false))
	if len(keys) != 3 {
		t.Errorf("secret keys = %v", keys)
	}
}

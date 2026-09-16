// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestAirflowConnectionDataSourceSchemaMatchesModel(t *testing.T) {
	ctx := t.Context()
	s := dataSourceSchema(t, NewAirflowConnectionDataSource()).Schema

	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if d := state.Set(ctx, airflowConnectionNullModel()); d.HasError() {
		t.Fatalf("airflowConnectionModel does not fit the data-source schema: %v", d.Errors())
	}
	for name, attr := range s.Attributes {
		if name == "airflow" || name == "conn_id" {
			continue
		}
		if !attr.IsComputed() {
			t.Errorf("data-source attribute %q should be Computed", name)
		}
	}
}

// The by-conn_id lookup is exercised end to end as part of
// TestAccAirflowConnectionResource, which already stands up an environment, a
// database and a bucket — standing a second set up only to read one of them
// back would double the slowest acceptance test in the suite for no extra
// coverage.

// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAirflowDataSourceSchemaMatchesModel(t *testing.T) {
	ctx := t.Context()
	s := dataSourceSchema(t, NewAirflowDataSource()).Schema

	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if d := state.Set(ctx, airflowNullModel()); d.HasError() {
		t.Fatalf("airflowModel does not fit the data-source schema: %v", d.Errors())
	}
	// The data source shares the resource's model, so every attribute must be
	// readable. An attribute the API never reports would be dead schema.
	for name, attr := range s.Attributes {
		if name == "env" || name == "name" {
			continue
		}
		if !attr.IsComputed() {
			t.Errorf("data-source attribute %q should be Computed", name)
		}
	}
}

// TestAccAirflowDataSource creates an environment then looks it up by name.
// Skipped without HYPERFLUID_CREDENTIALS.
func TestAccAirflowDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_airflow" "etl" {
  env       = data.hyperfluid_env.default.id
  name      = "tf-acc-ds-af"
  node_tier = "micro"
}

data "hyperfluid_airflow" "by_name" {
  env        = data.hyperfluid_env.default.id
  name       = hyperfluid_airflow.etl.name
  depends_on = [hyperfluid_airflow.etl]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_airflow.by_name", "name", "tf-acc-ds-af"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_airflow.by_name", "id", "hyperfluid_airflow.etl", "id"),
					// The tier is recovered from the resolved cpu/memory, which
					// is the only thing the API reports back.
					resource.TestCheckResourceAttr("data.hyperfluid_airflow.by_name", "node_tier", "micro"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_airflow.by_name", "dag_bucket"),
				),
			},
		},
	})
}

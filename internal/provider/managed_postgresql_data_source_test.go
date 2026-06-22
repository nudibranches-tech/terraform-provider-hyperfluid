// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccManagedPostgresqlDataSource creates a cluster then looks it up by name.
// Skipped without HYPERFLUID_CREDENTIALS.
func TestAccManagedPostgresqlDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_managed_postgresql" "db" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-ds-pg"
  database_name    = "appdb"
  engine           = "postgresql"
  version          = "17"
  node_tier        = "nano"
  storage_capacity = 5
  configuration    = "standalone"
}

data "hyperfluid_managed_postgresql" "by_name" {
  env        = data.hyperfluid_env.default.id
  name       = hyperfluid_managed_postgresql.db.name
  depends_on = [hyperfluid_managed_postgresql.db]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_managed_postgresql.by_name", "name", "tf-acc-ds-pg"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_managed_postgresql.by_name", "id", "hyperfluid_managed_postgresql.db", "id"),
					resource.TestCheckResourceAttr("data.hyperfluid_managed_postgresql.by_name", "database_name", "appdb"),
				),
			},
		},
	})
}

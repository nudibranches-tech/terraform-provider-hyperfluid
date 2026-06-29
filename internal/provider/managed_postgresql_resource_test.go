// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccManagedPostgresql covers a cluster + a user: create → read → update
// (storage_capacity) → import (both). Skipped unless HYPERFLUID_CREDENTIALS is
// set (testAccPreCheck), so it is a no-op in CI without cluster access.
func TestAccManagedPostgresql(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccManagedPostgresqlConfig(1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "name", "tf-acc-pg"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "storage_capacity", "1"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "node_tier", "nano"),
					// omitted from config above → asserts the default is false (private).
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "expose_to_internet", "false"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "backup_policy", "manual"),
					resource.TestCheckResourceAttrSet("hyperfluid_managed_postgresql.db", "write_endpoint"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql_user.editor", "username", "app_editor"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql_user.editor", "permission_level", "editor"),
				),
			},
			{
				// in-place storage growth
				Config: testAccManagedPostgresqlConfig(2),
				Check:  resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "storage_capacity", "2"),
			},
			{
				ResourceName:      "hyperfluid_managed_postgresql.db",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "hyperfluid_managed_postgresql_user.editor",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					cluster := s.RootModule().Resources["hyperfluid_managed_postgresql.db"]
					user := s.RootModule().Resources["hyperfluid_managed_postgresql_user.editor"]
					return fmt.Sprintf("%s/%s", cluster.Primary.ID, user.Primary.ID), nil
				},
			},
		},
	})
}

func testAccManagedPostgresqlConfig(storageGB int) string {
	return fmt.Sprintf(`
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_managed_postgresql" "db" {
  env           = data.hyperfluid_env.default.id
  name             = "tf-acc-pg"
  database_name    = "appdb"
  engine           = "postgresql"
  version          = "17"
  node_tier        = "nano"
  storage_capacity = %d
  configuration    = "standalone"
}

resource "hyperfluid_managed_postgresql_user" "editor" {
  managed_postgresql = hyperfluid_managed_postgresql.db.id
  username           = "app_editor"
  permission_level   = "editor"
}
`, storageGB)
}

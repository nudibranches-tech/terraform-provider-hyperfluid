// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccManagedPostgresqlUserDataSource creates a cluster + user then looks the
// user up by username within the cluster. Skipped without HYPERFLUID_CREDENTIALS.
func TestAccManagedPostgresqlUserDataSource(t *testing.T) {
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
  name             = "tf-acc-ds-pgu"
  database_name    = "appdb"
  engine           = "postgresql"
  version          = "17"
  node_tier        = "nano"
  storage_capacity = 5
  configuration    = "standalone"
}

resource "hyperfluid_managed_postgresql_user" "editor" {
  managed_postgresql = hyperfluid_managed_postgresql.db.id
  username           = "app_editor"
  permission_level   = "editor"
}

data "hyperfluid_managed_postgresql_user" "by_name" {
  managed_postgresql = hyperfluid_managed_postgresql.db.id
  username           = hyperfluid_managed_postgresql_user.editor.username
  depends_on         = [hyperfluid_managed_postgresql_user.editor]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_managed_postgresql_user.by_name", "username", "app_editor"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_managed_postgresql_user.by_name", "id", "hyperfluid_managed_postgresql_user.editor", "id"),
					resource.TestCheckResourceAttr("data.hyperfluid_managed_postgresql_user.by_name", "permission_level", "editor"),
				),
			},
		},
	})
}

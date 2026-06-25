// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccStorageZoneDataSource looks up the primary ("default") zone, which
// every org has, and asserts it resolves as primary + enabled. Skipped without
// HYPERFLUID_CREDENTIALS.
func TestAccStorageZoneDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_storage_zone" "primary" {
  zone_id = "default"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_storage_zone.primary", "zone_id", "default"),
					resource.TestCheckResourceAttr("data.hyperfluid_storage_zone.primary", "primary", "true"),
					resource.TestCheckResourceAttr("data.hyperfluid_storage_zone.primary", "enabled", "true"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_storage_zone.primary", "name"),
				),
			},
		},
	})
}

// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccBifrostDataSource reads the ambient Bifrost endpoints + feature flags,
// asserting the connection fields and the nested features block are populated.
// Skipped without HYPERFLUID_CREDENTIALS.
func TestAccBifrostDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "hyperfluid_bifrost" "this" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.hyperfluid_bifrost.this", "rest_url"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_bifrost.this", "pgwire_endpoint"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_bifrost.this", "graphql_url_template"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_bifrost.this", "features.graphql_enabled"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_bifrost.this", "features.postgresql_enabled"),
				),
			},
		},
	})
}

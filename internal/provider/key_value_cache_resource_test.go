// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccKeyValueCacheResource covers create → read → update (maxmemory) →
// import → destroy. Skipped unless HYPERFLUID_CREDENTIALS is set.
func TestAccKeyValueCacheResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccKeyValueCacheConfig("256mb"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_key_value_cache.kv", "name", "tf-acc-kv"),
					resource.TestCheckResourceAttr("hyperfluid_key_value_cache.kv", "maxmemory", "256mb"),
					resource.TestCheckResourceAttrSet("hyperfluid_key_value_cache.kv", "host"),
					resource.TestCheckResourceAttrSet("hyperfluid_key_value_cache.kv", "credentials_secret_name"),
				),
			},
			{
				Config: testAccKeyValueCacheConfig("512mb"),
				Check:  resource.TestCheckResourceAttr("hyperfluid_key_value_cache.kv", "maxmemory", "512mb"),
			},
			{
				ResourceName:      "hyperfluid_key_value_cache.kv",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccKeyValueCacheConfig(maxmemory string) string {
	return `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_key_value_cache" "kv" {
  env           = data.hyperfluid_env.default.id
  name             = "tf-acc-kv"
  maxmemory        = "` + maxmemory + `"
  maxmemory_policy = "allkeys-lru"
}
`
}

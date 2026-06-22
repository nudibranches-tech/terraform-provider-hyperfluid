// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccKeyValueCacheDataSource creates a cache then looks it up by name.
// Skipped without HYPERFLUID_CREDENTIALS.
func TestAccKeyValueCacheDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_key_value_cache" "kv" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-ds-kv"
  maxmemory        = "256mb"
  maxmemory_policy = "allkeys-lru"
}

data "hyperfluid_key_value_cache" "by_name" {
  env        = data.hyperfluid_env.default.id
  name       = hyperfluid_key_value_cache.kv.name
  depends_on = [hyperfluid_key_value_cache.kv]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_key_value_cache.by_name", "name", "tf-acc-ds-kv"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_key_value_cache.by_name", "id", "hyperfluid_key_value_cache.kv", "id"),
				),
			},
		},
	})
}

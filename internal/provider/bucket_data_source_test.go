// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccBucketDataSource creates a bucket then looks it up by name, asserting
// the data source resolves to the same id. Skipped without HYPERFLUID_CREDENTIALS.
func TestAccBucketDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_bucket" "test" {
  env  = data.hyperfluid_env.default.id
  name = "tf-acc-ds-bucket"
}

data "hyperfluid_bucket" "by_name" {
  env        = data.hyperfluid_env.default.id
  name       = hyperfluid_bucket.test.name
  depends_on = [hyperfluid_bucket.test]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_bucket.by_name", "name", "tf-acc-ds-bucket"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_bucket.by_name", "id", "hyperfluid_bucket.test", "id"),
					resource.TestCheckResourceAttr("data.hyperfluid_bucket.by_name", "ready", "true"),
					resource.TestCheckResourceAttr("data.hyperfluid_bucket.by_name", "storage_zone_id", "default"),
				),
			},
		},
	})
}

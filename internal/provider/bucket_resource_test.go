// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccBucketResource exercises the full create → read → import → destroy
// lifecycle against a live cluster. Skipped unless HYPERFLUID_CREDENTIALS is set
// (see testAccPreCheck), so it is a no-op in CI without cluster access.
func TestAccBucketResource(t *testing.T) {
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
  env = data.hyperfluid_env.default.id
  name   = "tf-acc-bucket"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_bucket.test", "name", "tf-acc-bucket"),
					resource.TestCheckResourceAttr("hyperfluid_bucket.test", "ready", "true"),
					// storage_zone_id is omitted → resolves to the primary zone.
					resource.TestCheckResourceAttr("hyperfluid_bucket.test", "storage_zone_id", "default"),
					resource.TestCheckResourceAttrSet("hyperfluid_bucket.test", "id"),
				),
			},
			{
				// Re-applying the same config (storage_zone_id omitted) must be a
				// no-op: the resolved "default" zone is held in state, so the bucket
				// is never planned for replacement. Guards against the Optional+Computed
				// plan-modifier ordering that would otherwise force perpetual recreate.
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_bucket" "test" {
  env = data.hyperfluid_env.default.id
  name   = "tf-acc-bucket"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      "hyperfluid_bucket.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

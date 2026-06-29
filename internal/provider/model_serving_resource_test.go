// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccModelServingResource exercises create → read → update (replicas) →
// import → destroy against a live cluster. Skipped unless HYPERFLUID_CREDENTIALS
// is set (testAccPreCheck), so it is a no-op in CI without cluster access.
// Uses a small embedding model so the serving provisions quickly.
func TestAccModelServingResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccModelServingConfig(1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_model_serving.test", "model_id", "BAAI/bge-large-en-v1.5"),
					resource.TestCheckResourceAttr("hyperfluid_model_serving.test", "model_type", "embedding"),
					resource.TestCheckResourceAttr("hyperfluid_model_serving.test", "runtime", "vllm"),
					resource.TestCheckResourceAttr("hyperfluid_model_serving.test", "replicas", "1"),
					resource.TestCheckResourceAttr("hyperfluid_model_serving.test", "phase", "Ready"),
					resource.TestCheckResourceAttrSet("hyperfluid_model_serving.test", "name"),
					resource.TestCheckResourceAttrSet("hyperfluid_model_serving.test", "endpoint"),
				),
			},
			{
				// in-place scaling — the only patchable field.
				Config: testAccModelServingConfig(2),
				Check:  resource.TestCheckResourceAttr("hyperfluid_model_serving.test", "replicas", "2"),
			},
			{
				ResourceName:      "hyperfluid_model_serving.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccModelServingConfig(replicas int) string {
	return `
resource "hyperfluid_model_serving" "test" {
  display_name = "tf-acc-embed"
  model_id     = "BAAI/bge-large-en-v1.5"
  model_type   = "embedding"
  runtime      = "vllm"
  gpu          = 1
  replicas     = ` + strconv.Itoa(replicas) + `
}
`
}

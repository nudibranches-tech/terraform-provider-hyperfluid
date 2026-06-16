// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccContainerAppResource exercises create → read → update (replicas) →
// import → destroy against a live cluster. Skipped unless HYPERFLUID_CREDENTIALS
// is set (testAccPreCheck), so it is a no-op in CI without cluster access.
func TestAccContainerAppResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccContainerAppConfig(1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "name", "tf-acc-app"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "replicas", "1"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "phase", "Ready"),
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.test", "id"),
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.test", "endpoint"),
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.test", "cpu_request"),
				),
			},
			{
				// in-place update via the resource_version (H4) PATCH path.
				Config: testAccContainerAppConfig(2),
				Check:  resource.TestCheckResourceAttr("hyperfluid_container_app.test", "replicas", "2"),
			},
			{
				ResourceName:      "hyperfluid_container_app.test",
				ImportState:       true,
				ImportStateVerify: true,
				// resource_tier is not returned by the API (M2), so it cannot be
				// recovered on import; the rest round-trips.
				ImportStateVerifyIgnore: []string{"resource_tier"},
			},
		},
	})
}

func testAccContainerAppConfig(replicas int) string {
	return `
data "hyperfluid_harbor" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "test" {
  harbor           = data.hyperfluid_harbor.default.id
  name             = "tf-acc-app"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  port             = 8080
  replicas         = ` + strconv.Itoa(replicas) + `
  resource_tier    = "nano"
}
`
}

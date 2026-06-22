// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccContainerAppDataSource creates an app then looks it up by name.
// Skipped without HYPERFLUID_CREDENTIALS.
func TestAccContainerAppDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "test" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-ds-app"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  port             = 8080
  replicas         = 1
  resource_tier    = "nano"
}

data "hyperfluid_container_app" "by_name" {
  env        = data.hyperfluid_env.default.id
  name       = hyperfluid_container_app.test.name
  depends_on = [hyperfluid_container_app.test]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_container_app.by_name", "name", "tf-acc-ds-app"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_container_app.by_name", "id", "hyperfluid_container_app.test", "id"),
					resource.TestCheckResourceAttr("data.hyperfluid_container_app.by_name", "image_repository", "nginxinc/nginx-unprivileged"),
				),
			},
		},
	})
}

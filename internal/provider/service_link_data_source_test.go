// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccServiceLinkDataSource reads back a link created in the same config.
// Skipped unless HYPERFLUID_CREDENTIALS is set.
func TestAccServiceLinkDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccServiceLinkDataSourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_service_link.found", "consumer.kind", "ContainerApp"),
					resource.TestCheckResourceAttr("data.hyperfluid_service_link.found", "target.kind", "HfKeyValueCache"),
					resource.TestCheckResourceAttr("data.hyperfluid_service_link.found", "ready", "true"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_service_link.found", "ports.0.port"),
				),
			},
		},
	})
}

const testAccServiceLinkDataSourceConfig = `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "web" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-sl-ds-web"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
  ]
  resource_tier    = "nano"
}

resource "hyperfluid_key_value_cache" "cache" {
  env  = data.hyperfluid_env.default.id
  name = "tf-acc-sl-ds-cache"
}

resource "hyperfluid_service_link" "web_to_cache" {
  env      = data.hyperfluid_env.default.id
  consumer = { kind = "ContainerApp", name = hyperfluid_container_app.web.slug }
  target   = { kind = "HfKeyValueCache", name = hyperfluid_key_value_cache.cache.slug }
}

data "hyperfluid_service_link" "found" {
  env  = data.hyperfluid_env.default.id
  name = hyperfluid_service_link.web_to_cache.name
}
`

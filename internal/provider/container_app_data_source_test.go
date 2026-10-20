// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccContainerAppDataSource creates an app then looks it up by slug, and by
// the deprecated name.
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
  slug             = "tf-acc-ds-app"
  display_name     = "TF acceptance data source app"
  tags             = ["tf-acc"]
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
  ]
  replicas         = 1
  resource_tier    = "nano"
}

data "hyperfluid_container_app" "by_slug" {
  env        = data.hyperfluid_env.default.id
  slug       = hyperfluid_container_app.test.slug
  depends_on = [hyperfluid_container_app.test]
}

data "hyperfluid_container_app" "by_name" {
  env        = data.hyperfluid_env.default.id
  name       = hyperfluid_container_app.test.slug
  depends_on = [hyperfluid_container_app.test]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_container_app.by_slug", "slug", "tf-acc-ds-app"),
					resource.TestCheckResourceAttr("data.hyperfluid_container_app.by_slug", "name", "tf-acc-ds-app"),
					resource.TestCheckResourceAttr("data.hyperfluid_container_app.by_slug", "display_name", "TF acceptance data source app"),
					resource.TestCheckResourceAttr("data.hyperfluid_container_app.by_slug", "tags.0", "tf-acc"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_container_app.by_slug", "id", "hyperfluid_container_app.test", "id"),
					resource.TestCheckResourceAttr("data.hyperfluid_container_app.by_name", "slug", "tf-acc-ds-app"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_container_app.by_name", "id", "hyperfluid_container_app.test", "id"),
					resource.TestCheckResourceAttr("data.hyperfluid_container_app.by_name", "image_repository", "nginxinc/nginx-unprivileged"),
				),
			},
		},
	})
}

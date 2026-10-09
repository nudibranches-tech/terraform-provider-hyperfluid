// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
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
					// omitted from config above → asserts the default is false (private).
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "expose_to_internet", "false"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "phase", "Ready"),
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.test", "id"),
					// Private app: there is no public endpoint to report, which is the
					// point of the expose_to_internet default asserted above.
					resource.TestCheckNoResourceAttr("hyperfluid_container_app.test", "endpoint"),
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.test", "cpu_request"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "ports.#", "1"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "ports.0.name", "http"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "ports.0.port", "8080"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "ports.0.primary", "true"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "ports.0.protocol", "HTTP"),
					// `port` is deprecated but still computed: the platform reports
					// the primary port there.
					resource.TestCheckResourceAttr("hyperfluid_container_app.test", "port", "8080"),
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.test", "slug"),
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
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "test" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-app"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
  ]
  replicas         = ` + strconv.Itoa(replicas) + `
  resource_tier    = "nano"
}
`
}

// TestAccContainerAppGitSource deploys from a GitHub repository holding a
// hyperfluid.toml with an entry named after the app, read with a Git credential
// as every source is. Skipped unless HYPERFLUID_TEST_GIT_REPO (e.g.
// "acme/orders-api", on github.com) and HYPERFLUID_TEST_GIT_TOKEN (a token that
// can read it) are set on top of the credentials the other acceptance tests need.
func TestAccContainerAppGitSource(t *testing.T) {
	repo := os.Getenv("HYPERFLUID_TEST_GIT_REPO")
	token := os.Getenv("HYPERFLUID_TEST_GIT_TOKEN")
	config := `
data "hyperfluid_env" "default" {
  name = "default"
}

variable "git_token" {
  type      = string
  sensitive = true
  default   = "` + token + `"
}

resource "hyperfluid_secret" "git" {
  name             = "tf-acc-git-source"
  secret_type      = "scm_credential"
  value            = jsonencode({ provider = "github", token = var.git_token })
  value_wo_version = "1"
}

resource "hyperfluid_container_app" "git" {
  env  = data.hyperfluid_env.default.id
  name = "tf-acc-git-app"
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
  ]
  resource_tier = "nano"

  image_source {
    git {
      provider   = "github"
      repository = "` + repo + `"
      credential = hyperfluid_secret.git.name
    }
  }
}
`
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if repo == "" || token == "" {
				t.Skip("HYPERFLUID_TEST_GIT_REPO or HYPERFLUID_TEST_GIT_TOKEN not set; skipping Git source acceptance test")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.git", "resolved_image"),
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.git", "revision"),
					resource.TestCheckResourceAttrSet("hyperfluid_container_app.git", "last_synced_at"),
					// The resolved image never lands in the literal-image attributes.
					resource.TestCheckNoResourceAttr("hyperfluid_container_app.git", "image_repository"),
					// Nothing the config left out is written back.
					resource.TestCheckNoResourceAttr("hyperfluid_container_app.git", "image_source.git.branch"),
					resource.TestCheckNoResourceAttr("hyperfluid_container_app.git", "image_source.git.interval"),
				),
			},
			{
				// A release made through Git is not drift: re-planning shows nothing.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

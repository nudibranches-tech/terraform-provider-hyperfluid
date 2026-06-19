// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccBackupTargetResource covers create → read → update (description) →
// import → destroy. Skipped unless HYPERFLUID_CREDENTIALS is set; the S3
// endpoint + secret names come from env so the test is environment-agnostic.
func TestAccBackupTargetResource(t *testing.T) {
	endpoint := os.Getenv("HYPERFLUID_TEST_S3_ENDPOINT")
	akSecret := os.Getenv("HYPERFLUID_TEST_S3_ACCESS_KEY_SECRET")
	skSecret := os.Getenv("HYPERFLUID_TEST_S3_SECRET_KEY_SECRET")
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if endpoint == "" || akSecret == "" || skSecret == "" {
				t.Skip("HYPERFLUID_TEST_S3_* not set; skipping backup_target acceptance test")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccBackupTargetConfig(endpoint, akSecret, skSecret, "first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "name", "tf-acc-bt"),
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "phase", "Ready"),
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "description", "first"),
				),
			},
			{
				Config: testAccBackupTargetConfig(endpoint, akSecret, skSecret, "second"),
				Check:  resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "description", "second"),
			},
			{
				ResourceName:      "hyperfluid_backup_target.bt",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccBackupTargetConfig(endpoint, akSecret, skSecret, desc string) string {
	return `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_backup_target" "bt" {
  env                        = data.hyperfluid_env.default.id
  name                          = "tf-acc-bt"
  endpoint_url                  = "` + endpoint + `"
  destination_path              = "s3://default-backup/tf-acc/"
  access_key_secret_name        = "` + akSecret + `"
  secret_access_key_secret_name = "` + skSecret + `"
  insecure                      = true
  description                   = "` + desc + `"
}
`
}

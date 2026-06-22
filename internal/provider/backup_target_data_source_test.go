// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccBackupTargetDataSource creates a backup target then looks it up by name.
// Like the resource test, it needs real S3 (HYPERFLUID_TEST_S3_*) so the target
// can become Ready; skipped otherwise.
func TestAccBackupTargetDataSource(t *testing.T) {
	endpoint := os.Getenv("HYPERFLUID_TEST_S3_ENDPOINT")
	akSecret := os.Getenv("HYPERFLUID_TEST_S3_ACCESS_KEY_SECRET")
	skSecret := os.Getenv("HYPERFLUID_TEST_S3_SECRET_KEY_SECRET")
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if endpoint == "" || akSecret == "" || skSecret == "" {
				t.Skip("HYPERFLUID_TEST_S3_* not set; skipping backup_target data source test")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_backup_target" "bt" {
  env                           = data.hyperfluid_env.default.id
  name                          = "tf-acc-ds-bt"
  endpoint_url                  = "` + endpoint + `"
  destination_path              = "s3://default-backup/tf-acc-ds/"
  access_key_secret_name        = "` + akSecret + `"
  secret_access_key_secret_name = "` + skSecret + `"
  insecure                      = true
}

data "hyperfluid_backup_target" "by_name" {
  env        = data.hyperfluid_env.default.id
  name       = hyperfluid_backup_target.bt.name
  depends_on = [hyperfluid_backup_target.bt]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_backup_target.by_name", "name", "tf-acc-ds-bt"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_backup_target.by_name", "id", "hyperfluid_backup_target.bt", "id"),
				),
			},
		},
	})
}

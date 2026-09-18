// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

func TestBackupTargetSchema(t *testing.T) {
	assertSchemaImplementation(t, NewBackupTargetResource())
}

func TestBackupTargetRetentionBounds(t *testing.T) {
	s := resourceSchema(t, NewBackupTargetResource())
	p := path.Root("retention_days")
	cfg := nullConfig(t, s, nil)

	for _, days := range []int64{0, -1, 36} {
		if d := validateInt64(t, s, cfg, p, types.Int64Value(days)); !d.HasError() {
			t.Errorf("retention_days = %d was accepted", days)
		}
	}
	for _, days := range []int64{1, 7, 14, 35} {
		if d := validateInt64(t, s, cfg, p, types.Int64Value(days)); d.HasError() {
			t.Errorf("retention_days = %d was refused: %v", days, d)
		}
	}
}

// TestAccBackupTargetResource covers create → read → update (description +
// retention_days) → import → destroy. Skipped unless HYPERFLUID_CREDENTIALS is
// set; the S3 endpoint + secret names come from env so the test is
// environment-agnostic.
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
				Config: testAccBackupTargetConfig(endpoint, akSecret, skSecret, "first", 7),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "name", "tf-acc-bt"),
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "phase", "Ready"),
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "description", "first"),
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "retention_days", "7"),
				),
			},
			{
				Config: testAccBackupTargetConfig(endpoint, akSecret, skSecret, "second", 21),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					// Retention is patched onto the target, never a reason to rebuild
					// it — a replacement would drop every backup it holds.
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hyperfluid_backup_target.bt", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "description", "second"),
					resource.TestCheckResourceAttr("hyperfluid_backup_target.bt", "retention_days", "21"),
				),
			},
			{
				ResourceName:      "hyperfluid_backup_target.bt",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccBackupTargetConfig(endpoint, akSecret, skSecret, desc string, retentionDays int) string {
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
  retention_days                = ` + strconv.Itoa(retentionDays) + `
  description                   = "` + desc + `"
}
`
}

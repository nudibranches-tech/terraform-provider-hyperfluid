// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccSecretResource covers create → read (value absent from state) →
// rotate (value + value_wo_version) → import → destroy, plus the data source.
// Skipped unless HYPERFLUID_CREDENTIALS is set.
func TestAccSecretResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccSecretConfig("s3cr3t", "1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_secret.s", "name", "tf-acc-secret"),
					resource.TestCheckResourceAttr("hyperfluid_secret.s", "secret_type", "plaintext"),
					resource.TestCheckResourceAttrSet("hyperfluid_secret.s", "secret_path"),
					// value is write-only — must be null in state.
					resource.TestCheckNoResourceAttr("hyperfluid_secret.s", "value"),
					// data source resolves the same secret by name.
					resource.TestCheckResourceAttrPair(
						"data.hyperfluid_secret.lookup", "id",
						"hyperfluid_secret.s", "id"),
				),
			},
			{
				// rotation: new value + bumped version.
				Config: testAccSecretConfig("rotated", "2"),
				Check:  resource.TestCheckResourceAttr("hyperfluid_secret.s", "value_wo_version", "2"),
			},
			{
				ResourceName:      "hyperfluid_secret.s",
				ImportState:       true,
				ImportStateVerify: true,
				// write-only / client-only fields can't be recovered on import.
				ImportStateVerifyIgnore: []string{"value", "value_wo_version"},
			},
		},
	})
}

func testAccSecretConfig(value, version string) string {
	return `
resource "hyperfluid_secret" "s" {
  name             = "tf-acc-secret"
  secret_type      = "plaintext"
  value            = "` + value + `"
  value_wo_version = "` + version + `"
  tags             = ["env:test"]
}

data "hyperfluid_secret" "lookup" {
  name       = "tf-acc-secret"
  depends_on = [hyperfluid_secret.s]
}
`
}

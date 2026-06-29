// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccAiApiKeyResource exercises create → read → import → destroy against a
// live cluster. Skipped unless HYPERFLUID_CREDENTIALS is set (testAccPreCheck).
// The secret `key` and `expires_in_days` are not returned by the API, so they
// are ignored on import verification.
func TestAccAiApiKeyResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccAiApiKeyConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_ai_api_key.test", "name", "tf-acc-key"),
					resource.TestCheckResourceAttr("hyperfluid_ai_api_key.test", "scopes.0", "model:*"),
					resource.TestCheckResourceAttrSet("hyperfluid_ai_api_key.test", "id"),
					resource.TestCheckResourceAttrSet("hyperfluid_ai_api_key.test", "key"),
					resource.TestCheckResourceAttrSet("hyperfluid_ai_api_key.test", "key_prefix"),
				),
			},
			{
				ResourceName:            "hyperfluid_ai_api_key.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"key", "expires_in_days"},
			},
		},
	})
}

const testAccAiApiKeyConfig = `
resource "hyperfluid_ai_api_key" "test" {
  name            = "tf-acc-key"
  scopes          = ["model:*"]
  expires_in_days = 30
}
`

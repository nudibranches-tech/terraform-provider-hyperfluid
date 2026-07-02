// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccBucketCredentialsDataSource creates a bucket then mints its S3
// credentials, asserting the endpoint/keys are populated. Skipped without
// HYPERFLUID_CREDENTIALS.
func TestAccBucketCredentialsDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_bucket" "test" {
  env  = data.hyperfluid_env.default.id
  name = "tf-acc-ds-bucket-creds"
}

data "hyperfluid_bucket_credentials" "creds" {
  env         = data.hyperfluid_env.default.id
  bucket_name = hyperfluid_bucket.test.name
  depends_on  = [hyperfluid_bucket.test]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.hyperfluid_bucket_credentials.creds", "id"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_bucket_credentials.creds", "access_key"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_bucket_credentials.creds", "secret_key"),
					resource.TestCheckResourceAttrSet("data.hyperfluid_bucket_credentials.creds", "endpoint"),
				),
			},
		},
	})
}

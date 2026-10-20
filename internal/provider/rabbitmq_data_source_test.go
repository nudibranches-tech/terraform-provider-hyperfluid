// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccRabbitMQDataSource creates a broker then looks it up by name.
// Skipped without HYPERFLUID_CREDENTIALS.
func TestAccRabbitMQDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_rabbitmq" "mq" {
  env  = data.hyperfluid_env.default.id
  name = "tf-acc-ds-mq"
}

data "hyperfluid_rabbitmq" "by_name" {
  env        = data.hyperfluid_env.default.id
  name       = hyperfluid_rabbitmq.mq.name
  depends_on = [hyperfluid_rabbitmq.mq]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperfluid_rabbitmq.by_name", "name", "tf-acc-ds-mq"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_rabbitmq.by_name", "id", "hyperfluid_rabbitmq.mq", "id"),
					resource.TestCheckResourceAttrPair("data.hyperfluid_rabbitmq.by_name", "password_secret_name", "hyperfluid_rabbitmq.mq", "password_secret_name"),
				),
			},
		},
	})
}

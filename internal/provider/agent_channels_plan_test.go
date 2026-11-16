// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// TestAccAgentChannelPlans drives a Tchap channel, the channel limits and the
// Slack manifest through the real Terraform CLI against the fake console (no
// Hyperfluid environment, no real credentials). It proves what only a plan
// shows: the gateway's status moving on its own (phase, device, last event,
// message) plans nothing, a new relogin_trigger is an in-place update that logs
// the bot in once, imports verify, and destroy removes the channel and resets
// the limits.
func TestAccAgentChannelPlans(t *testing.T) {
	fc, _ := newFakeChannels(t)
	creds := fakeConsoleCredentials(t)
	password, recovery := uuid.NewString(), uuid.NewString()
	matrix, limits := "hyperfluid_agent_matrix_channel.staff", "hyperfluid_agent_channel_limits.this"

	config := func(trigger string) string {
		return fmt.Sprintf(`
provider "hyperfluid" {
  endpoint         = %q
  organization_id  = %q
  credentials_file = %q
}

resource "hyperfluid_agent_matrix_channel" "staff" {
  agent_id               = %[4]q
  preset                 = "tchap"
  user_id                = "@bot-support:example.org"
  password_secret_id     = %[5]q
  recovery_key_secret_id = %[6]q
  allowed_users          = ["@*:example.org"]
  direct_messages        = true
  relogin_trigger        = %[7]q
}

resource "hyperfluid_agent_channel_limits" "this" {
  agent_id           = %[4]q
  questions_per_hour = 200
  questions_per_day  = 1000
}

data "hyperfluid_agent_slack_manifest" "this" {
  agent_id = %[4]q
}
`, fc.baseURL, fc.org, creds, fc.agent, password, recovery, trigger)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() {},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if fc.matrix != nil || fc.limits != nil {
				return fmt.Errorf("destroy left the Matrix channel (%v) or the limits (%v)", fc.matrix != nil, fc.limits != nil)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(matrix, "phase", "Connecting"),
					resource.TestCheckResourceAttr(matrix, "configured", "true"),
					resource.TestCheckResourceAttr(matrix, "password_secret_id", password),
					resource.TestCheckNoResourceAttr(matrix, "device_id"),
					resource.TestCheckResourceAttr(limits, "questions_per_hour", "200"),
					resource.TestCheckResourceAttr("data.hyperfluid_agent_slack_manifest.this", "app_token_scope", "connections:write"),
				),
			},
			{
				// The bot connected, logged its device and saw events since: all
				// of it the gateway's, none of it a change to plan.
				PreConfig: func() {
					fc.phase, fc.device, fc.message = console.AiAgentChannelPhaseDtoConnected, "DEVICEA", "in 2 rooms"
					fc.lastEvent = fc.lastEvent.Add(3 * time.Hour)
				},
				Config: config("1"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(matrix, "phase", "Connected"),
					resource.TestCheckResourceAttr(matrix, "device_id", "DEVICEA"),
					resource.TestCheckResourceAttr(matrix, "last_event_at", "2026-11-16T12:00:00Z"),
				),
			},
			{
				PreConfig: func() { fc.phase, fc.required = console.AiAgentChannelPhaseDtoNeedsLogin, true },
				Config:    config("2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(matrix, plancheck.ResourceActionUpdate)},
				},
				Check: func(*terraform.State) error {
					if fc.relogins != 1 || len(fc.calls("PUT", `/channels/matrix`)) != 1 {
						return fmt.Errorf("relogins = %d, PUTs = %d: want one re-login and no new PUT", fc.relogins, len(fc.calls("PUT", `/channels/matrix`)))
					}
					return nil
				},
			},
			{
				ResourceName:            matrix,
				ImportState:             true,
				ImportStateId:           fc.agent.String(),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"relogin_trigger"},
			},
			{
				ResourceName:      limits,
				ImportState:       true,
				ImportStateId:     fc.agent.String(),
				ImportStateVerify: true,
			},
		},
	})
}

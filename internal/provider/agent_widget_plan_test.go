// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccAgentWidgetPlans drives hyperfluid_agent_widget through the real
// Terraform CLI against the fake console (no Hyperfluid environment, no real
// credentials: the token endpoint is a stub too). It proves what only a plan
// shows: a console upgrade that moves the pinned script path plans nothing, an
// edit of the widget's look keeps widget_id and embed_snippet known, a new
// rotation trigger plans a new id, import, and that destroy disables.
//
// It needs a Terraform CLI, so it runs with TF_ACC like the acceptance tests,
// but it needs no credentials.
func TestAccAgentWidgetPlans(t *testing.T) {
	fw, _ := newFakeWidget(t)
	creds := fakeConsoleCredentials(t)
	addr := "hyperfluid_agent_widget.site"

	config := func(title, trigger string) string {
		triggerLine := ""
		if trigger != "" {
			triggerLine = fmt.Sprintf("  rotation_trigger = %q\n", trigger)
		}
		return fmt.Sprintf(`
provider "hyperfluid" {
  endpoint         = %q
  organization_id  = %q
  credentials_file = %q
}

resource "hyperfluid_agent_widget" "site" {
  agent_id        = %q
  allowed_origins = ["https://www.Example.org/"]
  title           = %q
  colour          = "#0B6E69"
  lang            = "fr"
  suggestions     = ["Which datasets?", "How fresh?"]
  privacy_url     = "https://www.example.org/privacy"
%s}
`, fw.serverURL(), fw.org, creds, fw.agent, title, triggerLine)
	}
	snippet := fmt.Sprintf(`<script defer src="%s/widget/v1/agent.js" data-agent="wa_first" data-lang="fr"></script>`, fw.serverURL())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() {},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if fw.enabled {
				return fmt.Errorf("the widget is still enabled after destroy")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("Data assistant", "first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "widget_id", "wa_first"),
					resource.TestCheckResourceAttr(addr, "phase", "Active"),
					resource.TestCheckResourceAttr(addr, "embed_snippet", snippet),
					resource.TestCheckResourceAttr(addr, "colour", "#0B6E69"),
					resource.TestCheckResourceAttr(addr, "allow_any_origin", "false"),
				),
			},
			{
				// A console upgrade: a new build, so a new pinned path and
				// integrity. The refresh records them; nothing is planned.
				PreConfig: func() { fw.build = "fedcba9876543210" },
				Config:    config("Data assistant", "first"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr(addr, "script_pinned_path", "/widget/v1.4.2/fedcba9876543210/agent.js"),
			},
			{
				// A new title updates in place, and the outputs a site template
				// reads stay known in the plan.
				Config: config("Open data assistant", "first"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("widget_id"), knownvalue.StringExact("wa_first")),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("embed_snippet"), knownvalue.StringExact(snippet)),
					},
				},
				Check: resource.TestCheckResourceAttr(addr, "title", "Open data assistant"),
			},
			{
				// A new trigger value: the id is unknown until the rotation.
				Config: config("Open data assistant", "second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("widget_id")),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("embed_snippet")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "widget_id", "wa_rotated1"),
					resource.TestMatchResourceAttr(addr, "embed_snippet", regexp.MustCompile(`data-agent="wa_rotated1"`)),
				),
			},
			{
				// Removing the trigger does not rotate.
				Config: config("Open data assistant", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("widget_id"), knownvalue.StringExact("wa_rotated1")),
					},
				},
				Check: func(*terraform.State) error {
					if fw.rotations != 1 {
						return fmt.Errorf("rotations = %d, want 1", fw.rotations)
					}
					return nil
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     fw.agent.String(),
				ImportStateVerify: true,
				// Spelled by the console on import ("#0b6e69", the origin as a
				// browser sends it); the configured spelling is kept otherwise.
				ImportStateVerifyIgnore: []string{"colour", "allowed_origins"},
			},
		},
	})
}

func (fw *fakeWidget) serverURL() string {
	return fw.baseURL
}

// fakeConsoleCredentials writes a service-account file whose token endpoint
// is a local stub, so the provider configures without any real identity.
func fakeConsoleCredentials(t *testing.T) string {
	t.Helper()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test", "token_type": "Bearer", "expires_in": 3600})
	}))
	t.Cleanup(tokens.Close)
	p := filepath.Join(t.TempDir(), "sa.json")
	raw, _ := json.Marshal(map[string]string{
		"client_id": "terraform-test", "client_secret": "not-a-secret", "token_uri": tokens.URL,
	})
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

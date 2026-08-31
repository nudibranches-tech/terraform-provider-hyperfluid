// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func port(protocol string, n int32) console.ServiceLinkPort {
	return console.ServiceLinkPort{Port: n, Protocol: protocol}
}

func TestFirstUndeclaredPort(t *testing.T) {
	declared := []console.ServiceLinkPort{port("TCP", 8080), port("UDP", 9000)}

	if got := firstUndeclaredPort([]console.ServiceLinkPort{port("TCP", 8080)}, declared); got != nil {
		t.Errorf("published port reported as undeclared: %v", got)
	}
	if got := firstUndeclaredPort([]console.ServiceLinkPort{port("UDP", 9000)}, declared); got != nil {
		t.Errorf("published port reported as undeclared: %v", got)
	}
	// Same number, wrong protocol: still undeclared. A bare port comparison
	// would wrongly let this through.
	if got := firstUndeclaredPort([]console.ServiceLinkPort{port("UDP", 8080)}, declared); got == nil {
		t.Error("8080/UDP accepted although only 8080/TCP is published")
	}
	// Reports the first offender, not the last.
	got := firstUndeclaredPort([]console.ServiceLinkPort{port("TCP", 1), port("TCP", 2)}, declared)
	if got == nil || got.Port != 1 {
		t.Errorf("expected the first offender (1), got %v", got)
	}
	if got := firstUndeclaredPort(nil, declared); got != nil {
		t.Errorf("empty request reported an offender: %v", got)
	}
	// Nothing published: anything asked for is undeclared.
	if got := firstUndeclaredPort([]console.ServiceLinkPort{port("TCP", 8080)}, nil); got == nil {
		t.Error("expected an offender when the app publishes nothing")
	}
}

func TestFormatPorts(t *testing.T) {
	if got := formatPorts(nil); got != "no ports" {
		t.Errorf("formatPorts(nil) = %q", got)
	}
	if got := formatPorts([]console.ServiceLinkPort{port("TCP", 8080), port("UDP", 9000)}); got != "TCP/8080, UDP/9000" {
		t.Errorf("formatPorts = %q", got)
	}
}

// TestAccServiceLinkResource covers a link to a fixed-port kind (no
// target_ports) and an app-to-app link with chosen ports, then import. Skipped
// unless HYPERFLUID_CREDENTIALS is set.
func TestAccServiceLinkResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccServiceLinkConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					// The name is derived by the platform from the linked pair.
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_db", "name", "tf-acc-sl-web-tf-acc-sl-db"),
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_db", "ready", "true"),
					resource.TestCheckResourceAttrSet("hyperfluid_service_link.web_to_db", "ports.0.port"),
					// A fixed-port kind gets its port opened without asking.
					resource.TestCheckNoResourceAttr("hyperfluid_service_link.web_to_db", "target_ports.0.port"),

					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_api", "target_ports.0.port", "8080"),
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_api", "target_ports.0.protocol", "TCP"),
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_api", "ports.0.port", "8080"),

					// The app's own ports attribute assigned straight through.
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_api_all", "ports.0.port", "8080"),
				),
			},
			{
				ResourceName: "hyperfluid_service_link.web_to_db",
				ImportState:  true,
				// target_ports is a choice the API never echoes back, so an
				// imported link cannot round-trip it.
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"target_ports"},
			},
		},
	})
}

// TestAccServiceLinkTargetPortsRejectedForFixedPortKind asserts the config-time
// check fires before any API call, so the user is not left with a bare 400.
func TestAccServiceLinkTargetPortsRejectedForFixedPortKind(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccServiceLinkBadKindConfig,
				ExpectError: regexp.MustCompile(`target_ports may only be set when target.kind is ContainerApp`),
			},
		},
	})
}

const testAccServiceLinkConfig = `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "web" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-sl-web"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  port             = 8080
  resource_tier    = "nano"
}

resource "hyperfluid_container_app" "api" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-sl-api"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  port             = 8080
  resource_tier    = "nano"
}

resource "hyperfluid_managed_postgresql" "db" {
  env  = data.hyperfluid_env.default.id
  name = "tf-acc-sl-db"
}

resource "hyperfluid_service_link" "web_to_db" {
  env      = data.hyperfluid_env.default.id
  consumer = { kind = "ContainerApp", name = hyperfluid_container_app.web.slug }
  target   = { kind = "ManagedPostgreSQL", name = hyperfluid_managed_postgresql.db.slug }
}

resource "hyperfluid_service_link" "web_to_api" {
  env          = data.hyperfluid_env.default.id
  consumer     = { kind = "ContainerApp", name = hyperfluid_container_app.web.slug }
  target       = { kind = "ContainerApp", name = hyperfluid_container_app.api.slug }
  target_ports = [{ port = 8080 }]
}

resource "hyperfluid_service_link" "web_to_api_all" {
  env          = data.hyperfluid_env.default.id
  consumer     = { kind = "ContainerApp", name = hyperfluid_container_app.api.slug }
  target       = { kind = "ContainerApp", name = hyperfluid_container_app.web.slug }
  target_ports = hyperfluid_container_app.web.ports
}
`

const testAccServiceLinkBadKindConfig = `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_service_link" "bad" {
  env          = data.hyperfluid_env.default.id
  consumer     = { kind = "ContainerApp", name = "tf-acc-sl-web" }
  target       = { kind = "ManagedPostgreSQL", name = "tf-acc-sl-db" }
  target_ports = [{ port = 5432 }]
}
`

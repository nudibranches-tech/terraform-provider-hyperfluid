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

func TestL4Protocol(t *testing.T) {
	// HTTP is carried over TCP, so an app's HTTP port can be assigned straight to
	// target_ports and still match what the platform opens.
	if got := l4Protocol("HTTP"); got != "TCP" {
		t.Errorf("l4Protocol(HTTP) = %q, want TCP", got)
	}
	for _, p := range []string{"TCP", "UDP", "SCTP"} {
		if got := l4Protocol(p); got != p {
			t.Errorf("l4Protocol(%s) = %q, want unchanged", p, got)
		}
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
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_cache", "name", "tf-acc-sl-web-tf-acc-sl-cache"),
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_cache", "ready", "true"),
					resource.TestCheckResourceAttrSet("hyperfluid_service_link.web_to_cache", "ports.0.port"),
					// A fixed-port kind gets its port opened without asking.
					resource.TestCheckNoResourceAttr("hyperfluid_service_link.web_to_cache", "target_ports.0.port"),

					// A non-primary port, which the deprecated single `port` cannot express.
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_api", "target_ports.0.port", "9090"),
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_api", "target_ports.0.protocol", "TCP"),
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_api", "ports.0.port", "9090"),

					// The multi-port app round-trips its declared ports.
					resource.TestCheckResourceAttr("hyperfluid_container_app.api", "ports.#", "2"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.api", "ports.0.name", "http"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.api", "ports.0.primary", "true"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.api", "ports.1.name", "metrics"),
					// The wire pair (TCP + no appProtocol) collapses back to the single kind.
					resource.TestCheckResourceAttr("hyperfluid_container_app.api", "ports.1.protocol", "TCP"),
					resource.TestCheckResourceAttr("hyperfluid_container_app.api", "ports.0.protocol", "HTTP"),

					// The app's own ports attribute assigned straight through.
					resource.TestCheckResourceAttr("hyperfluid_service_link.web_to_api_all", "ports.0.port", "8080"),
				),
			},
			{
				ResourceName: "hyperfluid_service_link.web_to_cache",
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
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
  ]
  resource_tier    = "nano"
}

resource "hyperfluid_container_app" "api" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-sl-api"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  resource_tier    = "nano"

  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
    { name = "metrics", port = 9090, protocol = "TCP" },
  ]
}

resource "hyperfluid_key_value_cache" "cache" {
  env  = data.hyperfluid_env.default.id
  name = "tf-acc-sl-cache"
}

resource "hyperfluid_service_link" "web_to_cache" {
  env      = data.hyperfluid_env.default.id
  consumer = { kind = "ContainerApp", name = hyperfluid_container_app.web.slug }
  target   = { kind = "HfKeyValueCache", name = hyperfluid_key_value_cache.cache.slug }
}

resource "hyperfluid_service_link" "web_to_api" {
  env          = data.hyperfluid_env.default.id
  consumer     = { kind = "ContainerApp", name = hyperfluid_container_app.web.slug }
  target       = { kind = "ContainerApp", name = hyperfluid_container_app.api.slug }
  target_ports = [{ port = 9090 }]
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
  target       = { kind = "HfKeyValueCache", name = "tf-acc-sl-cache" }
  target_ports = [{ port = 6379 }]
}
`

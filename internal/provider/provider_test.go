// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// testAccProtoV6ProviderFactories instantiates the provider during acceptance
// testing. The factory is called per Terraform CLI command.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"hyperfluid": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccPreCheck skips acceptance tests when no credentials are configured, so
// they pass (as skips) in CI without cluster access and run locally when
// HYPERFLUID_CREDENTIALS points at a service-account JSON.
func testAccPreCheck(t *testing.T) {
	if os.Getenv("HYPERFLUID_CREDENTIALS") == "" {
		t.Skip("HYPERFLUID_CREDENTIALS not set; skipping acceptance test")
	}
}

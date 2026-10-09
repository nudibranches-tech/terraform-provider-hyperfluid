// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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

// TestSecretScmCredentialValidation: a Git credential's shape is checked at plan
// time, where the write-only value is visible in the config.
func TestSecretScmCredentialValidation(t *testing.T) {
	ctx := t.Context()
	r, ok := NewSecretResource().(*secretResource)
	if !ok {
		t.Fatal("NewSecretResource is not a *secretResource")
	}
	s := resourceSchema(t, r).Schema

	for _, tc := range []struct {
		name       string
		secretType string
		value      tftypes.Value
		wantErr    bool
	}{
		{"a valid credential", "scm_credential", tftypes.NewValue(tftypes.String, `{"provider":"github","token":"t"}`), false},
		{"a credential without a token", "scm_credential", tftypes.NewValue(tftypes.String, `{"provider":"github"}`), true},
		{"a credential that is not JSON", "scm_credential", tftypes.NewValue(tftypes.String, `ghp_x`), true},
		{"a value not yet known", "scm_credential", tftypes.NewValue(tftypes.String, tftypes.UnknownValue), false},
		{"no value", "scm_credential", tftypes.NewValue(tftypes.String, nil), false},
		{"another type is not judged", "plaintext", tftypes.NewValue(tftypes.String, `{"provider":"nope"}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := nullConfig(t, s, map[string]tftypes.Value{
				"secret_type": tftypes.NewValue(tftypes.String, tc.secretType),
				"value":       tc.value,
			})
			resp := &fwresource.ValidateConfigResponse{}
			r.ValidateConfig(ctx, fwresource.ValidateConfigRequest{Config: cfg}, resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Errorf("error = %v, want %v (%v)", got, tc.wantErr, resp.Diagnostics)
			}
		})
	}

	if d := validateString(t, s, nullConfig(t, s, nil), path.Root("secret_type"), types.StringValue("scm_credential")); d.HasError() {
		t.Errorf("secret_type = scm_credential must be accepted: %v", d)
	}
	if d := validateString(t, s, nullConfig(t, s, nil), path.Root("secret_type"), types.StringValue("oci_registry_config")); !d.HasError() {
		t.Error("secret_type = oci_registry_config is not supported yet and must be refused")
	}
}

// TestAccSecretResourceScmCredential creates a Git credential: the value stays
// out of state, like any other secret's.
func TestAccSecretResourceScmCredential(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "hyperfluid_secret" "git" {
  name             = "tf-acc-git"
  secret_type      = "scm_credential"
  value            = jsonencode({ provider = "github", token = "ghp_acceptance_not_a_real_token" })
  value_wo_version = "1"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_secret.git", "secret_type", "scm_credential"),
					resource.TestCheckNoResourceAttr("hyperfluid_secret.git", "value"),
				),
			},
		},
	})
}

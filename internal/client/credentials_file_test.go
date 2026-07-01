// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCredentialsPathIn pins the cross-repo path layout for both the Linux and
// macOS user-config dirs. hfctl (Rust) writes to dirs::config_dir()/hyperfluid/
// terraform/credentials.json; the provider must land on the byte-identical path
// for each OS's config dir or it silently never finds what hfctl wrote.
func TestCredentialsPathIn(t *testing.T) {
	cases := map[string]struct {
		configDir string
		want      string
	}{
		"linux": {
			configDir: "/home/alice/.config",
			want:      "/home/alice/.config/hyperfluid/terraform/credentials.json",
		},
		"macos": {
			configDir: "/Users/alice/Library/Application Support",
			want:      "/Users/alice/Library/Application Support/hyperfluid/terraform/credentials.json",
		},
		// Windows %AppData%. Expressed with forward slashes so filepath.Join +
		// filepath.FromSlash resolve the separator correctly on any host OS — the
		// suffix-joining is what credentialsPathIn owns; the backslash reality of
		// os.UserConfigDir is Go stdlib we trust.
		"windows": {
			configDir: "C:/Users/alice/AppData/Roaming",
			want:      "C:/Users/alice/AppData/Roaming/hyperfluid/terraform/credentials.json",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := credentialsPathIn(tc.configDir)
			want := filepath.FromSlash(tc.want)
			if got != want {
				t.Fatalf("credentialsPathIn(%q) = %q, want %q", tc.configDir, got, want)
			}
		})
	}
}

// TestDefaultCredentialsPathLinux checks DefaultCredentialsPath honors the
// XDG_CONFIG_HOME base on Linux (the macOS layout is covered by the pure
// credentialsPathIn case above, since os.UserConfigDir ignores XDG there).
func TestDefaultCredentialsPathLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME layout is Linux-specific")
	}
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	got, err := DefaultCredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "hyperfluid", "terraform", "credentials.json")
	if got != want {
		t.Fatalf("DefaultCredentialsPath() = %q, want %q", got, want)
	}
}

// TestNewFromServiceAccountToleratesHfctlHandoffKeys pins the cross-repo contract:
// the file `hfctl tf auth` writes carries an extra non-secret `profile` key (plus
// org/org_id/api_url) beyond a downloaded service_account.json, and the provider
// must parse it without complaint. This guards against a future parser change
// (e.g. adding DisallowUnknownFields) silently desyncing from hfctl
// (nudibranches-tech/hyperfluid#2862, #2873).
func TestNewFromServiceAccountToleratesHfctlHandoffKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	// Byte-shaped like what `hfctl tf auth` materializes, including `profile`.
	content := `{
  "profile": "prod-readonly",
  "client_id": "cid",
  "client_secret": "sekret",
  "auth_uri": "https://kc/auth",
  "token_uri": "https://kc/token",
  "issuer": "https://kc",
  "org": "acme",
  "org_id": "00000000-0000-0000-0000-000000000001",
  "api_url": "https://console.example"
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// endpoint "" → falls back to api_url from the file.
	_, orgID, err := NewFromServiceAccount("", path)
	if err != nil {
		t.Fatalf("provider rejected the hfctl handoff file: %v", err)
	}
	if orgID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("orgID = %q, want the org_id from the file", orgID)
	}
}

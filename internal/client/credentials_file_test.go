// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
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

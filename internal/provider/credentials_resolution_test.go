// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// TestResolveCredentialsPath exercises the four-rung credential resolution order
// (nudibranches-tech/hyperfluid#2862): explicit block path > HYPERFLUID_CREDENTIALS
// > the well-known file hfctl writes > none.
func TestResolveCredentialsPath(t *testing.T) {
	t.Run("explicit block path wins over env", func(t *testing.T) {
		t.Setenv("HYPERFLUID_CREDENTIALS", "/from/env.json")
		if got := resolveCredentialsPath("/explicit.json"); got != "/explicit.json" {
			t.Fatalf("got %q, want /explicit.json", got)
		}
	})

	t.Run("env when no explicit path", func(t *testing.T) {
		t.Setenv("HYPERFLUID_CREDENTIALS", "/from/env.json")
		if got := resolveCredentialsPath(""); got != "/from/env.json" {
			t.Fatalf("got %q, want /from/env.json", got)
		}
	})

	t.Run("well-known file when it exists", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("home-based config dir assumed")
		}
		t.Setenv("HYPERFLUID_CREDENTIALS", "")
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", home) // Linux
		t.Setenv("HOME", home)            // macOS: $HOME/Library/Application Support

		want, err := client.DefaultCredentialsPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(want, []byte(`{"client_id":"x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolveCredentialsPath(""); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("none present yields empty", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("home-based config dir assumed")
		}
		t.Setenv("HYPERFLUID_CREDENTIALS", "")
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", home)
		t.Setenv("HOME", home)
		// No file created under the well-known path.
		if got := resolveCredentialsPath(""); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})
}

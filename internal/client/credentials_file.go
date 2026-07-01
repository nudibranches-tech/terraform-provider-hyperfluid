// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"os"
	"path/filepath"
)

// DefaultCredentialsPath returns the well-known path where `hfctl tf auth`
// materializes the active Terraform credential:
//
//	<user-config-dir>/hyperfluid/terraform/credentials.json
//
// It resolves <user-config-dir> with os.UserConfigDir so the path agrees with
// hfctl's Rust dirs::config_dir() on every OS — Linux $XDG_CONFIG_HOME (else
// ~/.config), macOS ~/Library/Application Support, Windows %AppData%. This is
// the cross-repo contract (nudibranches-tech/hyperfluid#2862); do NOT hardcode
// ~/.config, or the provider would look in the wrong place on macOS/Windows and
// never find what hfctl wrote.
func DefaultCredentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return credentialsPathIn(dir), nil
}

// credentialsPathIn joins the fixed hyperfluid/terraform/credentials.json suffix
// onto a user-config dir. Split out from DefaultCredentialsPath so the path
// layout can be unit-tested for each OS's config dir without stubbing
// os.UserConfigDir.
func credentialsPathIn(configDir string) string {
	return filepath.Join(configDir, "hyperfluid", "terraform", "credentials.json")
}

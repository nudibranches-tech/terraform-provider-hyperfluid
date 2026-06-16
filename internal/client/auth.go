// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

// Package client is the HTTP client for the Hyperfluid Console external API.
//
// NOTE (M0): this is a small hand-written client covering only what the
// `bucket` resource and `harbor` data source need. At M0.5 the request/response
// types and method bodies are REPLACED with code generated from
// console-external.openapi.json via oapi-codegen (tools/fetch-spec.sh +
// `make generate`). The exported Client surface is intentionally stable so the
// resource layer barely changes when that swap happens.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/oauth2/clientcredentials"
)

// serviceAccount mirrors the JSON the console produces, using the same field
// names hfctl already consumes so a single downloaded file serves both tools.
//
// Today's "create service account" download is creds-only:
//
//	client_id, client_secret, auth_uri, token_uri, issuer
//
// The planned "download all" button adds the connection context that hfctl
// otherwise sets via `hfctl config set` — api_url, org_id/org, harbor — making
// the file fully self-describing (zero extra provider config). When those keys
// are absent, the provider falls back to its endpoint/organization_id args.
type serviceAccount struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
	TokenURI     string `json:"token_uri"`
	Issuer       string `json:"issuer"`

	// Optional context (present only in the all-in-one download).
	APIURL string `json:"api_url"`
	OrgID  string `json:"org_id"`
	Org    string `json:"org"`
	Harbor string `json:"harbor"`
}

// NewFromServiceAccount parses the SA JSON at credsPath, sets up an
// auto-refreshing OIDC client_credentials token source, and returns an
// authenticated Client plus the organization id found in the credential.
//
// The client_secret never leaves this process: it is exchanged for short-lived
// bearer tokens and is never written to Terraform state.
func NewFromServiceAccount(ctx context.Context, endpoint, credsPath string) (*Client, string, error) {
	raw, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, "", fmt.Errorf("read credentials file %q: %w", credsPath, err)
	}
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, "", fmt.Errorf("parse credentials file %q: %w", credsPath, err)
	}
	if sa.ClientID == "" || sa.ClientSecret == "" || sa.TokenURI == "" {
		return nil, "", fmt.Errorf("credentials file %q missing client_id/client_secret/token_uri", credsPath)
	}

	cfg := clientcredentials.Config{
		ClientID:     sa.ClientID,
		ClientSecret: sa.ClientSecret,
		TokenURL:     sa.TokenURI,
	}

	base := endpoint
	if base == "" {
		base = sa.APIURL // present only in the all-in-one download
	}
	if base == "" {
		return nil, "", fmt.Errorf("no API endpoint: set the provider `endpoint`, HYPERFLUID_ENDPOINT, or `api_url` in the credentials file")
	}

	c := &Client{
		http:    cfg.Client(ctx), // token source refreshes automatically
		baseURL: strings.TrimRight(base, "/"),
	}

	// org_id wins over the human-readable org slug when both are present.
	orgID := sa.OrgID
	if orgID == "" {
		orgID = sa.Org
	}
	return c, orgID, nil
}

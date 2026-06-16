// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

// Package client is a thin, stable wrapper over the generated Console API
// client (internal/console, produced by oapi-codegen from
// console-external.openapi.json via `make generate`). It owns auth and exposes
// a small resource-facing surface so the generated code can be regenerated
// without churning the resource layer.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/oauth2/clientcredentials"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
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
//
// The token source is anchored to context.Background(), NOT a request context:
// the provider's Configure context is canceled once Configure returns, so a
// request-scoped token source would fail every later API call with
// "context canceled". Per-call cancellation still works — each API call passes
// its own context to the generated client methods.
func NewFromServiceAccount(endpoint, credsPath string) (*Client, string, error) {
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

	// cfg.Client returns an *http.Client whose transport injects (and refreshes)
	// the bearer token automatically.
	api, err := console.NewClientWithResponses(
		strings.TrimRight(base, "/"),
		console.WithHTTPClient(cfg.Client(context.Background())),
	)
	if err != nil {
		return nil, "", fmt.Errorf("build console client: %w", err)
	}

	// org_id wins over the human-readable org slug when both are present.
	orgID := sa.OrgID
	if orgID == "" {
		orgID = sa.Org
	}
	return &Client{api: api}, orgID, nil
}

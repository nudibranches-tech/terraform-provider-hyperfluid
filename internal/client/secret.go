// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// BuildSecretValue builds the `value` payload the API expects for a given
// secret_type. The request body's value is an untyped `interface{}`, so we
// construct it directly.
//
// plaintext and json are sent as a `{type, value}` envelope; scm_credential is
// sent as the bare credential object, because the platform validates that
// shape itself and refuses an envelope. The oci_registry_config type is not
// supported yet.
func BuildSecretValue(secretType, valueStr string) (interface{}, error) {
	switch secretType {
	case "plaintext":
		return map[string]interface{}{"type": "plaintext", "value": valueStr}, nil
	case "json":
		var v interface{}
		if err := json.Unmarshal([]byte(valueStr), &v); err != nil {
			return nil, fmt.Errorf("secret_type=json requires `value` to be valid JSON: %w", err)
		}
		return map[string]interface{}{"type": "json", "value": v}, nil
	case "scm_credential":
		return buildScmCredentialValue(valueStr)
	default:
		return nil, fmt.Errorf("secret_type %q is not supported yet (use plaintext, json or scm_credential)", secretType)
	}
}

// scmProviders are the Git hosts a credential can belong to.
var scmProviders = []string{"github", "gitlab", "forgejo"}

// buildScmCredentialValue checks a Git credential's JSON and returns it in the
// shape the platform stores: `{provider, username?, base_url?, token}`.
//
// Errors never quote a value: the input carries the token.
func buildScmCredentialValue(valueStr string) (map[string]interface{}, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(valueStr), &raw); err != nil {
		return nil, fmt.Errorf("secret_type=scm_credential requires `value` to be a JSON object " +
			"{\"provider\", \"token\", \"username\"?, \"base_url\"?}")
	}
	for key := range raw {
		switch key {
		case "provider", "username", "base_url", "token":
		default:
			return nil, fmt.Errorf("secret_type=scm_credential: unknown key %q (expected provider, username, base_url, token)", key)
		}
	}
	str := func(key string) (string, bool, error) {
		field, ok := raw[key]
		if !ok {
			return "", false, nil
		}
		var v *string
		if err := json.Unmarshal(field, &v); err != nil {
			return "", false, fmt.Errorf("secret_type=scm_credential: %q must be a string", key)
		}
		if v == nil {
			return "", false, nil
		}
		return *v, true, nil
	}

	provider, _, err := str("provider")
	if err != nil {
		return nil, err
	}
	if !slices.Contains(scmProviders, provider) {
		return nil, fmt.Errorf("secret_type=scm_credential: \"provider\" must be one of %s", strings.Join(scmProviders, ", "))
	}
	token, _, err := str("token")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("secret_type=scm_credential: \"token\" is required")
	}
	out := map[string]interface{}{"provider": provider, "token": token}

	// Blank fields are left out, not sent empty, so the provider's own default
	// applies.
	username, _, err := str("username")
	if err != nil {
		return nil, err
	}
	if username = strings.TrimSpace(username); username != "" {
		out["username"] = username
	}
	baseURL, _, err := str("base_url")
	if err != nil {
		return nil, err
	}
	if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
		out["base_url"] = baseURL
	}
	return out, nil
}

func (c *Client) CreateSecret(ctx context.Context, orgID string, body console.CreateSecretRequestBody) (*console.SecretMetadataResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateSecretWithResponse(ctx, org, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create secret", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create secret: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetSecret(ctx context.Context, orgID, id string) (*console.SecretMetadataResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	sid, err := parseUUID("id", id)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetSecretWithResponse(ctx, org, sid)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get secret", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get secret: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) UpdateSecret(ctx context.Context, orgID, id string, body console.UpdateSecretRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	sid, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.UpdateSecretWithResponse(ctx, org, sid, body)
	if err != nil {
		return err
	}
	return statusErr("update secret", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteSecret(ctx context.Context, orgID, id string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	sid, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteSecretWithResponse(ctx, org, sid)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete secret", resp.StatusCode(), resp.Body)
}

// FindSecretByName returns the metadata of the secret with the given name, or
// ErrNotFound. Used by the data source.
func (c *Client) FindSecretByName(ctx context.Context, orgID, name string) (*console.SecretMetadataResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListSecretsWithResponse(ctx, org, &console.ListSecretsParams{Name: &name})
	if err != nil {
		return nil, err
	}
	if err := statusErr("list secrets", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, ErrNotFound
	}
	return findByName(resp.JSON200.Secrets, name, func(s *console.SecretMetadataResponse) string {
		return s.Name
	})
}

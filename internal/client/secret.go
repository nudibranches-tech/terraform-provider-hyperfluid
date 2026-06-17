// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// BuildSecretValue builds the `{type, value}` payload the API expects for a
// given secret_type. The request body's value is an untyped `interface{}`
// (inline oneOf), so we construct the map directly.
//
// M1 supports plaintext and json; the structured types (oci_registry_config,
// scm_credential) are a follow-up.
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
	default:
		return nil, fmt.Errorf("secret_type %q is not supported yet (use plaintext or json)", secretType)
	}
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
	if resp.JSON200 != nil {
		for _, s := range resp.JSON200.Secrets {
			if s.Name == name {
				meta := s
				return &meta, nil
			}
		}
	}
	return nil, ErrNotFound
}

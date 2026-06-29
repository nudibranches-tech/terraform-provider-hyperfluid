// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// API keys are org-scoped LLM-gateway credentials. There is no GET-by-id
// endpoint, so reads list and match on id. The secret `key` is returned only
// once, by Create.

func (c *Client) CreateApiKey(ctx context.Context, orgID string, body console.CreateApiKeyRequest) (*console.CreateApiKeyResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateApiKeyWithResponse(ctx, org, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create api key", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create api key: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) ListApiKeys(ctx context.Context, orgID string) ([]console.ApiKeyResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListApiKeysWithResponse(ctx, org)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list api keys", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// GetApiKey returns a key's metadata by id, or ErrNotFound (no GET-by-id route,
// so it lists and matches).
func (c *Client) GetApiKey(ctx context.Context, orgID, keyID string) (*console.ApiKeyResponse, error) {
	keys, err := c.ListApiKeys(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		if keys[i].Id.String() == keyID {
			return &keys[i], nil
		}
	}
	return nil, ErrNotFound
}

// FindApiKeyByName resolves a key by name, or ErrNotFound. Backs the data source.
func (c *Client) FindApiKeyByName(ctx context.Context, orgID, name string) (*console.ApiKeyResponse, error) {
	keys, err := c.ListApiKeys(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return findByName(keys, name, func(k *console.ApiKeyResponse) string { return k.Name })
}

func (c *Client) RevokeApiKey(ctx context.Context, orgID, keyID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	key, err := parseUUID("id", keyID)
	if err != nil {
		return err
	}
	resp, err := c.api.RevokeApiKeyWithResponse(ctx, org, key)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("revoke api key", resp.StatusCode(), resp.Body)
}

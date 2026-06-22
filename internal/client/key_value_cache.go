// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func (c *Client) CreateKeyValueCache(ctx context.Context, orgID, harborID string, body console.CreateHfKeyValueCacheCrdRequestBody) (*console.HfKeyValueCacheResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateHfKeyValueCacheCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create key-value cache", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create key-value cache: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetKeyValueCache(ctx context.Context, orgID, cacheID string) (*console.HfKeyValueCacheResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	cache, err := parseUUID("id", cacheID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetHfKeyValueCacheWithResponse(ctx, org, cache)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get key-value cache", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get key-value cache: empty response")
	}
	return resp.JSON200, nil
}

// FindKeyValueCache resolves a cache's id by name within an environment, or
// ErrNotFound. Backs the data source.
func (c *Client) FindKeyValueCache(ctx context.Context, orgID, harborID, name string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.ListHfKeyValueCachesWithResponse(ctx, org, harbor)
	if err != nil {
		return "", err
	}
	if err := statusErr("list key-value caches", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", ErrNotFound
	}
	m, err := findByName(*resp.JSON200, name, func(x *console.HfKeyValueCacheResponse) string {
		return x.Name
	})
	if err != nil {
		return "", err
	}
	return m.Id.String(), nil
}

func (c *Client) PatchKeyValueCache(ctx context.Context, orgID, cacheID string, body console.PatchHfKeyValueCacheCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	cache, err := parseUUID("id", cacheID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchHfKeyValueCacheCrdWithResponse(ctx, org, cache, body)
	if err != nil {
		return err
	}
	return statusErr("patch key-value cache", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteKeyValueCache(ctx context.Context, orgID, cacheID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	cache, err := parseUUID("id", cacheID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteHfKeyValueCacheCrdWithResponse(ctx, org, cache)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete key-value cache", resp.StatusCode(), resp.Body)
}

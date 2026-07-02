// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Bifrost (data source) ─────────────────────────────────────────────────
//
// Bifrost is the cluster's query layer; its connection endpoints and feature
// configuration are ambient (one per deployment, not org-scoped), so the data
// source reads them without any lookup key.

// GetBifrostInfo returns Bifrost's connection endpoints (REST / pgwire / GraphQL).
func (c *Client) GetBifrostInfo(ctx context.Context) (*console.BifrostInfoResponse, error) {
	resp, err := c.api.GetBifrostInfoWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get bifrost info", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get bifrost info: empty response")
	}
	return resp.JSON200, nil
}

// GetBifrostFeatures returns Bifrost's current feature configuration.
func (c *Client) GetBifrostFeatures(ctx context.Context) (*console.BifrostFeatureStatusResponse, error) {
	resp, err := c.api.GetBifrostFeaturesWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get bifrost features", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get bifrost features: empty response")
	}
	return resp.JSON200, nil
}

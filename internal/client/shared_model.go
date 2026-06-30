// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// Shared models are platform-level (not org-scoped): the catalog of models the
// org can consume. Read-only — backs the hyperfluid_shared_model data source.

func (c *Client) GetSharedModel(ctx context.Context, name string) (*console.SharedModelResponse, error) {
	resp, err := c.api.GetSharedModelWithResponse(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get shared model", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get shared model: empty response")
	}
	return resp.JSON200, nil
}

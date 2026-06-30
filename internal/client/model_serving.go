// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// Model servings are org-scoped and addressed by their CRD resource name (the
// server derives it from display_name on create and returns it in the response).

func (c *Client) CreateModelServing(ctx context.Context, orgID string, body console.CreateModelServingRequest) (*console.ModelServingResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateModelServingWithResponse(ctx, org, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create model serving", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create model serving: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetModelServing(ctx context.Context, orgID, name string) (*console.ModelServingResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetModelServingWithResponse(ctx, org, name)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get model serving", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get model serving: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) PatchModelServing(ctx context.Context, orgID, name string, body console.PatchModelServingRequest) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchModelServingWithResponse(ctx, org, name, body)
	if err != nil {
		return err
	}
	return statusErr("patch model serving", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteModelServing(ctx context.Context, orgID, name string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteModelServingWithResponse(ctx, org, name)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete model serving", resp.StatusCode(), resp.Body)
}

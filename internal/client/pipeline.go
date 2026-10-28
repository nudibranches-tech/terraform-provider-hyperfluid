// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// GetOrganizationSlug returns the organization's slug, which a pipeline
// request carries alongside its id.
func (c *Client) GetOrganizationSlug(ctx context.Context, orgID string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.GetOrganizationByIdWithResponse(ctx, org)
	if err != nil {
		return "", err
	}
	if err := statusErr("get organization", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", fmt.Errorf("hyperfluid: get organization: empty response")
	}
	return resp.JSON200.Slug, nil
}

// CreatePipeline posts a complete request body. It is sent as raw JSON since
// the request is an eleven-way union the provider passes through.
func (c *Client) CreatePipeline(ctx context.Context, body map[string]any) (*console.PipelineResponseData, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode pipeline: %w", err)
	}
	resp, err := c.api.CreatePipelineV2WithBodyWithResponse(ctx, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if err := statusErr("create pipeline", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create pipeline: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetPipeline(ctx context.Context, orgID, id string) (*console.PipelineDetailResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	pipeline, err := parseUUID("id", id)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetPipelineDetailWithResponse(ctx, org, pipeline)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get pipeline", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get pipeline: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) UpdatePipeline(ctx context.Context, orgID, id string, body console.UpdatePipelineRequest) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	pipeline, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.UpdatePipelineWithResponse(ctx, org, pipeline, body)
	if err != nil {
		return err
	}
	return statusErr("update pipeline", resp.StatusCode(), resp.Body)
}

func (c *Client) DeletePipeline(ctx context.Context, orgID, id string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	pipeline, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.DeletePipelineWithResponse(ctx, org, pipeline)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete pipeline", resp.StatusCode(), resp.Body)
}

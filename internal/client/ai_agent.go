// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Sapience AI agent ──────────────────────────────────────────────────────

func (c *Client) CreateAiAgent(ctx context.Context, orgID string, body console.CreateAiAgentRequest) (*console.AiAgentResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateAiAgentWithResponse(ctx, org, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create agent", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create agent: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetAiAgent(ctx context.Context, orgID, agentID string) (*console.AiAgentResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetAiAgentWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get agent", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get agent: empty response")
	}
	return resp.JSON200, nil
}

// FindAiAgent resolves an agent by name within an environment, or
// ErrNotFound. Agents are listed organization-wide.
func (c *Client) FindAiAgent(ctx context.Context, orgID, harborID, name string) (*console.AiAgentResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListAiAgentsWithResponse(ctx, org)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list agents", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, ErrNotFound
	}
	return find(*resp.JSON200, func(a *console.AiAgentResponse) bool {
		return a.HarborId == harbor && a.Name == name
	})
}

func (c *Client) UpdateAiAgent(ctx context.Context, orgID, agentID string, body console.UpdateAiAgentRequest) (*console.AiAgentResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.UpdateAiAgentWithResponse(ctx, org, id, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("update agent", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	return resp.JSON200, nil
}

func (c *Client) DeleteAiAgent(ctx context.Context, orgID, agentID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	id, err := parseUUID("id", agentID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteAiAgentWithResponse(ctx, org, id)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete agent", resp.StatusCode(), resp.Body)
}

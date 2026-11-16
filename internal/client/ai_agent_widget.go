// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Sapience AI agent: public web widget ──────────────────────────────────

// RefusedError is a 400 or a 409 from the console: the request was understood
// and refused, and Message is the console's reason, in its own words. The
// widget and channel routes use it for every rule that is the customer's to fix
// (an ineligible agent, a service account with write rights, a missing privacy
// URL, a secret that is not plaintext, a re-login before the bot's first
// device), so the caller can show the reason instead of a status code and a
// JSON body.
type RefusedError struct {
	Op      string
	Status  int
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("hyperfluid: %s refused: %s", e.Op, e.Message)
}

// refusedOr turns a 400 or a 409 into a RefusedError and leaves every other
// status to statusErr.
func refusedOr(op string, status int, body []byte) error {
	if status != http.StatusBadRequest && status != http.StatusConflict {
		return statusErr(op, status, body)
	}
	var parsed struct {
		Message string `json:"message"`
	}
	msg := string(bytes.TrimSpace(body))
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		msg = parsed.Message
	}
	return &RefusedError{Op: op, Status: status, Message: msg}
}

func agentIDs(orgID, agentID string) (openapi_types.UUID, openapi_types.UUID, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return org, org, err
	}
	id, err := parseUUID("agent_id", agentID)
	return org, id, err
}

// GetAiAgentWidget reads an agent's widget. A widget never enabled answers 200
// with phase NotConfigured; ErrNotFound means the agent itself is gone.
func (c *Client) GetAiAgentWidget(ctx context.Context, orgID, agentID string) (*console.AiAgentWidgetResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetAiAgentWidgetWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get agent widget", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get agent widget: empty response")
	}
	return resp.JSON200, nil
}

// EnableAiAgentWidget enables the widget with this configuration, or replaces
// the configuration of an enabled one. The widget id is minted on the first
// enable and kept after, through disable and enable again.
func (c *Client) EnableAiAgentWidget(ctx context.Context, orgID, agentID string, body console.AiAgentWidgetConfig) (*console.AiAgentWidgetResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.EnableAiAgentWidgetWithResponse(ctx, org, id, body)
	if err != nil {
		return nil, err
	}
	if err := refusedOr("enable agent widget", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: enable agent widget: empty response")
	}
	return resp.JSON200, nil
}

// UpdateAiAgentWidget replaces the configuration of a widget that is on and
// never turns one on: a widget that is off answers 409 (a RefusedError).
func (c *Client) UpdateAiAgentWidget(ctx context.Context, orgID, agentID string, body console.AiAgentWidgetConfig) (*console.AiAgentWidgetResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.UpdateAiAgentWidgetWithResponse(ctx, org, id, body)
	if err != nil {
		return nil, err
	}
	if err := refusedOr("update agent widget", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: update agent widget: empty response")
	}
	return resp.JSON200, nil
}

// RotateAiAgentWidget gives the widget a new id; every copy of the old tag
// gets 404 from then on.
func (c *Client) RotateAiAgentWidget(ctx context.Context, orgID, agentID string) (*console.AiAgentWidgetResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.RotateAiAgentWidgetWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := refusedOr("rotate agent widget", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: rotate agent widget: empty response")
	}
	return resp.JSON200, nil
}

// DisableAiAgentWidget is the kill switch: every copy of the tag gets 404. The
// id and the configuration are kept. An agent that is gone, or never had a
// widget, has nothing to disable, so a 404 is success.
func (c *Client) DisableAiAgentWidget(ctx context.Context, orgID, agentID string) error {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return err
	}
	resp, err := c.api.DisableAiAgentWidgetWithResponse(ctx, org, id)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return refusedOr("disable agent widget", resp.StatusCode(), resp.Body)
}

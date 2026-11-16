// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Sapience AI agent: chat channels and their shared limits ──────────────
//
// Every write answers the whole AiAgentChannelsResponse, so a caller maps its
// state from the write's own answer. A PUT replaces the block it names.

func channelsResult(op string, status int, body []byte, out *console.AiAgentChannelsResponse) (*console.AiAgentChannelsResponse, error) {
	if err := refusedOr(op, status, body); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("hyperfluid: %s: empty response", op)
	}
	return out, nil
}

// GetAiAgentChannels reads the chat channels of an agent, their state and the
// channel limits. ErrNotFound means the agent is gone.
func (c *Client) GetAiAgentChannels(ctx context.Context, orgID, agentID string) (*console.AiAgentChannelsResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetAiAgentChannelsWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	return channelsResult("get agent channels", resp.StatusCode(), resp.Body, resp.JSON200)
}

func (c *Client) SetAiAgentMatrixChannel(ctx context.Context, orgID, agentID string, body console.AiAgentMatrixChannelConfig) (*console.AiAgentChannelsResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.SetAiAgentMatrixChannelWithResponse(ctx, org, id, body)
	if err != nil {
		return nil, err
	}
	return channelsResult("set the Matrix channel", resp.StatusCode(), resp.Body, resp.JSON200)
}

// ReloginAiAgentMatrixChannel asks the operator for one more Matrix login. The
// console answers 409 (a RefusedError) without a Matrix channel or before the
// bot's first device.
func (c *Client) ReloginAiAgentMatrixChannel(ctx context.Context, orgID, agentID string) (*console.AiAgentChannelsResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ReloginAiAgentMatrixChannelWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	return channelsResult("request a Matrix re-login", resp.StatusCode(), resp.Body, resp.JSON200)
}

func (c *Client) SetAiAgentSlackChannel(ctx context.Context, orgID, agentID string, body console.AiAgentSlackChannelConfig) (*console.AiAgentChannelsResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.SetAiAgentSlackChannelWithResponse(ctx, org, id, body)
	if err != nil {
		return nil, err
	}
	return channelsResult("set the Slack channel", resp.StatusCode(), resp.Body, resp.JSON200)
}

func (c *Client) SetAiAgentDiscordChannel(ctx context.Context, orgID, agentID string, body console.AiAgentDiscordChannelConfig) (*console.AiAgentChannelsResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.SetAiAgentDiscordChannelWithResponse(ctx, org, id, body)
	if err != nil {
		return nil, err
	}
	return channelsResult("set the Discord channel", resp.StatusCode(), resp.Body, resp.JSON200)
}

func (c *Client) SetAiAgentChannelLimits(ctx context.Context, orgID, agentID string, body console.AiAgentChannelLimitsDto) (*console.AiAgentChannelsResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.SetAiAgentChannelLimitsWithResponse(ctx, org, id, body)
	if err != nil {
		return nil, err
	}
	return channelsResult("set the channel limits", resp.StatusCode(), resp.Body, resp.JSON200)
}

// AgentChannel names one chat channel for RemoveAiAgentChannel.
type AgentChannel string

const (
	ChannelMatrix  AgentChannel = "matrix"
	ChannelSlack   AgentChannel = "slack"
	ChannelDiscord AgentChannel = "discord"
)

// RemoveAiAgentChannel removes a channel: its block and its interface go in one
// write, and the operator tears that part of the gateway down. An agent that is
// gone has nothing to remove, so a 404 is success.
func (c *Client) RemoveAiAgentChannel(ctx context.Context, orgID, agentID string, channel AgentChannel) error {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return err
	}
	var (
		status int
		body   []byte
	)
	switch channel {
	case ChannelMatrix:
		resp, err := c.api.RemoveAiAgentMatrixChannelWithResponse(ctx, org, id)
		if err != nil {
			return err
		}
		status, body = resp.StatusCode(), resp.Body
	case ChannelSlack:
		resp, err := c.api.RemoveAiAgentSlackChannelWithResponse(ctx, org, id)
		if err != nil {
			return err
		}
		status, body = resp.StatusCode(), resp.Body
	case ChannelDiscord:
		resp, err := c.api.RemoveAiAgentDiscordChannelWithResponse(ctx, org, id)
		if err != nil {
			return err
		}
		status, body = resp.StatusCode(), resp.Body
	default:
		return fmt.Errorf("hyperfluid: unknown agent channel %q", channel)
	}
	if status == http.StatusNotFound {
		return nil
	}
	return refusedOr("remove the "+string(channel)+" channel", status, body)
}

// ResetAiAgentChannelLimits removes the agent's limits: the runtime's defaults
// apply again. A 404 (agent gone) is success.
func (c *Client) ResetAiAgentChannelLimits(ctx context.Context, orgID, agentID string) error {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return err
	}
	resp, err := c.api.ResetAiAgentChannelLimitsWithResponse(ctx, org, id)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return refusedOr("reset the channel limits", resp.StatusCode(), resp.Body)
}

// GetAiAgentSlackManifest returns the Slack app manifest for this agent, to
// create the Slack app its tokens come from.
func (c *Client) GetAiAgentSlackManifest(ctx context.Context, orgID, agentID string) (*console.AiAgentSlackManifestResponse, error) {
	org, id, err := agentIDs(orgID, agentID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetAiAgentSlackManifestWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get the Slack app manifest", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get the Slack app manifest: empty response")
	}
	return resp.JSON200, nil
}

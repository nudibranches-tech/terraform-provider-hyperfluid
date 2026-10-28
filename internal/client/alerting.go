// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// Alerting is organization-wide. The API has no GET for a single channel, rule
// or threshold, so each one is read by listing its collection.

// ── Notification channels ─────────────────────────────────────────────────

func (c *Client) ListAlertChannels(ctx context.Context, orgID string) ([]console.NotificationChannelDto, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListNotificationChannelsWithResponse(ctx, org)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list alert channels", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// GetAlertChannel returns the channel with that id, or ErrNotFound.
func (c *Client) GetAlertChannel(ctx context.Context, orgID, id string) (*console.NotificationChannelDto, error) {
	channels, err := c.ListAlertChannels(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return find(channels, func(ch *console.NotificationChannelDto) bool { return ch.Id.String() == id })
}

// FindAlertChannelByName returns the channel with that name, or ErrNotFound.
func (c *Client) FindAlertChannelByName(ctx context.Context, orgID, name string) (*console.NotificationChannelDto, error) {
	channels, err := c.ListAlertChannels(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return findByName(channels, name, func(ch *console.NotificationChannelDto) string { return ch.Name })
}

func (c *Client) CreateAlertChannel(ctx context.Context, orgID string, body console.ChannelBody) (*console.NotificationChannelDto, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateNotificationChannelWithResponse(ctx, org, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create alert channel", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: create alert channel: empty response")
	}
	return resp.JSON200, nil
}

// UpdateAlertChannel replaces the channel. A body without `url` keeps the
// stored webhook URL.
func (c *Client) UpdateAlertChannel(ctx context.Context, orgID, id string, body console.ChannelBody) (*console.NotificationChannelDto, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	cid, err := parseUUID("id", id)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.UpdateNotificationChannelWithResponse(ctx, org, cid, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("update alert channel", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: update alert channel: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) DeleteAlertChannel(ctx context.Context, orgID, id string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	cid, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteNotificationChannelWithResponse(ctx, org, cid)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete alert channel", resp.StatusCode(), resp.Body)
}

// ── Alert rules ───────────────────────────────────────────────────────────

func (c *Client) ListAlertRules(ctx context.Context, orgID string) ([]console.AlertRuleDto, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListAlertRulesWithResponse(ctx, org)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list alert rules", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// GetAlertRule returns the rule with that id, or ErrNotFound.
func (c *Client) GetAlertRule(ctx context.Context, orgID, id string) (*console.AlertRuleDto, error) {
	rules, err := c.ListAlertRules(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return find(rules, func(r *console.AlertRuleDto) bool { return r.Id.String() == id })
}

func (c *Client) CreateAlertRule(ctx context.Context, orgID string, body console.AlertRuleBody) (*console.AlertRuleDto, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateAlertRuleWithResponse(ctx, org, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create alert rule", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: create alert rule: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) UpdateAlertRule(ctx context.Context, orgID, id string, body console.AlertRuleBody) (*console.AlertRuleDto, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	rid, err := parseUUID("id", id)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.UpdateAlertRuleWithResponse(ctx, org, rid, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("update alert rule", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: update alert rule: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) DeleteAlertRule(ctx context.Context, orgID, id string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	rid, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteAlertRuleWithResponse(ctx, org, rid)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete alert rule", resp.StatusCode(), resp.Body)
}

// ── Capacity thresholds ───────────────────────────────────────────────────

// GetAlertThresholds returns the effective thresholds of signal, or ErrNotFound
// when the signal has none that can be overridden.
func (c *Client) GetAlertThresholds(ctx context.Context, orgID, signal string) (*console.SignalThresholdsDto, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListAlertThresholdsWithResponse(ctx, org)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list alert thresholds", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, ErrNotFound
	}
	return findByName(*resp.JSON200, signal, func(t *console.SignalThresholdsDto) string { return t.Signal })
}

// SetAlertThresholds overrides both tiers of signal.
func (c *Client) SetAlertThresholds(ctx context.Context, orgID, signal string, warningPct, criticalPct int32) (*console.SignalThresholdsDto, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.SetAlertThresholdsWithResponse(ctx, org, signal,
		console.SetThresholdsBody{WarningPct: warningPct, CriticalPct: criticalPct})
	if err != nil {
		return nil, err
	}
	if err := statusErr("set alert thresholds", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: set alert thresholds: empty response")
	}
	return resp.JSON200, nil
}

// ResetAlertThresholds drops the override of signal, which falls back to the
// platform defaults.
func (c *Client) ResetAlertThresholds(ctx context.Context, orgID, signal string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	resp, err := c.api.ClearAlertThresholdsWithResponse(ctx, org, signal)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("reset alert thresholds", resp.StatusCode(), resp.Body)
}

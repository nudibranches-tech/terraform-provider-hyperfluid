// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// QuotaExceededError is a 403 the console answers with `code: QUOTA_EXCEEDED`:
// the organization's plan already holds as many of ResourceType as it allows.
// It matches ErrForbidden too, so callers that only test for a denial still do.
type QuotaExceededError struct {
	Op           string
	ResourceType string
	Current      int64
	Limit        int64
	Message      string
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("hyperfluid: %s: %s", e.Op, e.Message)
}

func (e *QuotaExceededError) Is(target error) bool { return target == ErrForbidden }

// quotaErr reads a 403 body as a quota refusal, or returns nil when it is not one.
func quotaErr(op string, status int, body []byte) error {
	if status != http.StatusForbidden {
		return nil
	}
	var parsed struct {
		Code         string `json:"code"`
		Message      string `json:"message"`
		ResourceType string `json:"resource_type"`
		Current      int64  `json:"current"`
		Limit        int64  `json:"limit"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Code == "QUOTA_EXCEEDED" {
		return &QuotaExceededError{
			Op: op, ResourceType: parsed.ResourceType, Current: parsed.Current, Limit: parsed.Limit, Message: parsed.Message,
		}
	}
	return nil
}

// CreateEnvironment creates an environment and returns it once the platform
// reports it ready: the console waits for the Harbor CR before it answers.
func (c *Client) CreateEnvironment(ctx context.Context, orgID, name, slug string) (*console.Harbor, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateHarborCrdWithResponse(ctx, org, console.CreateHarborCrdRequestBody{Name: name, Slug: slug})
	if err != nil {
		return nil, err
	}
	const op = "create environment"
	if err := quotaErr(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if err := statusErr(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, errors.New("hyperfluid: create environment: empty response")
	}
	return resp.JSON201, nil
}

// GetEnvironmentBySlug returns the environment with that slug, or ErrNotFound.
func (c *Client) GetEnvironmentBySlug(ctx context.Context, orgID, slug string) (*console.Harbor, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListHarborsWithResponse(ctx, org, &console.ListHarborsParams{Slug: &slug})
	if err != nil {
		return nil, err
	}
	if err := statusErr("list environments", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, errors.New("hyperfluid: list environments: empty response")
	}
	return find(*resp.JSON200, func(h *console.Harbor) bool { return h.Slug == slug })
}

// DeleteEnvironment deletes an environment and everything in it. The console
// answers once the deletion is accepted; the environment disappears later.
func (c *Client) DeleteEnvironment(ctx context.Context, orgID, slug string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteHarborCrdWithResponse(ctx, org, slug)
	if err != nil {
		return err
	}
	return statusErr("delete environment", resp.StatusCode(), resp.Body)
}

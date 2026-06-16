// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ErrNotFound is returned when the API responds 404. Resources use it for drift
// detection (Read → remove from state) and delete confirmation (poll GET until
// ErrNotFound).
var ErrNotFound = errors.New("hyperfluid: resource not found")

// Client is a thin, stable wrapper over the generated Console API client
// (internal/console). Resources depend on this surface, not on the generated
// code directly, so regenerating the client never churns the resource layer.
type Client struct {
	api *console.ClientWithResponses
}

func parseUUID(field, s string) (openapi_types.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return openapi_types.UUID{}, fmt.Errorf("invalid %s %q: expected a UUID: %w", field, s, err)
	}
	return u, nil
}

// statusErr maps a response status to ErrNotFound / a body-carrying error / nil.
func statusErr(op string, status int, body []byte) error {
	switch {
	case status == http.StatusNotFound:
		return ErrNotFound
	case status >= 400:
		return fmt.Errorf("hyperfluid: %s -> %d: %s", op, status, bytes.TrimSpace(body))
	default:
		return nil
	}
}

// ── Harbor (data source) ──────────────────────────────────────────────────

func (c *Client) ListHarbors(ctx context.Context, orgID string) ([]console.Harbor, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListHarborsWithResponse(ctx, org, nil)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list harbors", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: list harbors: empty response")
	}
	return *resp.JSON200, nil
}

// ── Bucket (resource) ─────────────────────────────────────────────────────

// GetBucket reads the single-bucket detail view (HFBucketDetail) — the only
// shape that carries quota_gb/freeze_writes, per the read-mapping note.
func (c *Client) GetBucket(ctx context.Context, harborID, name string) (*console.HFBucketDetail, error) {
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetHarborBucketWithResponse(ctx, harbor, name)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get bucket", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get bucket %q: empty response", name)
	}
	return resp.JSON200, nil
}

func (c *Client) CreateBucket(ctx context.Context, harborID, name string) error {
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return err
	}
	resp, err := c.api.CreateHarborBucketWithResponse(ctx, harbor, console.CreateHFBucketRequest{Name: name})
	if err != nil {
		return err
	}
	return statusErr("create bucket", resp.StatusCode(), resp.Body)
}

func (c *Client) PatchBucket(ctx context.Context, harborID, name string, body console.PatchHFBucketRequest) error {
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchHarborBucketWithResponse(ctx, harbor, name, body)
	if err != nil {
		return err
	}
	return statusErr("patch bucket", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteBucket(ctx context.Context, harborID, name string) error {
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteHarborBucketWithResponse(ctx, harbor, name)
	if err != nil {
		return err
	}
	return statusErr("delete bucket", resp.StatusCode(), resp.Body)
}

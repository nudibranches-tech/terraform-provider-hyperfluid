// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ErrNotFound is returned when the API responds 404. Resources use it for drift
// detection (Read → remove from state) and delete confirmation (poll GET until
// ErrNotFound).
var ErrNotFound = errors.New("hyperfluid: resource not found")

// ErrForbidden is returned when the API responds 403. A delete-confirmation poll
// treats it as success: the console resolves a resource's authz scope by looking
// the resource up, so once it is gone the scope no longer resolves and the read
// is denied rather than answered with a 404.
var ErrForbidden = errors.New("hyperfluid: forbidden")

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

// forbiddenMessage renders a 403 the way hfctl does: the console phrases the
// body as "Missing permission '<key>' [on '<scope>']", and the caller usually
// cannot grant it themselves, so the permission and the person to ask are what
// matter. Falls back to the raw body for a 403 that is not that shape — a
// governance decision, say.
func forbiddenMessage(body []byte) string {
	var parsed struct {
		Message            string `json:"message"`
		RequiredPermission string `json:"required_permission"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Message == "" {
		return string(bytes.TrimSpace(body))
	}
	permission := parsed.RequiredPermission
	if permission == "" {
		if key, _ := quoted(parsed.Message); key != "" {
			permission = key
		}
	}
	if permission == "" {
		return parsed.Message
	}
	// The scope is the second quoted segment, absent for an org-wide denial.
	scope := ""
	if _, rest := quoted(parsed.Message); rest != "" {
		scope, _ = quoted(rest)
	}
	if scope != "" {
		return fmt.Sprintf("missing permission %q in %q; ask an organization administrator to grant it", permission, scope)
	}
	return fmt.Sprintf("missing permission %q; ask an organization administrator to grant it", permission)
}

// quoted returns the first single-quoted segment of s and the remainder after
// its closing quote.
func quoted(s string) (string, string) {
	start := strings.Index(s, "'")
	if start < 0 {
		return "", ""
	}
	rest := s[start+1:]
	end := strings.Index(rest, "'")
	if end < 0 {
		return "", ""
	}
	return rest[:end], rest[end+1:]
}

// statusErr maps a response status to ErrNotFound / a body-carrying error / nil.
func statusErr(op string, status int, body []byte) error {
	switch {
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusForbidden:
		return fmt.Errorf("%w: %s: %s", ErrForbidden, op, forbiddenMessage(body))
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

// FindEnv resolves an environment by slug or display name, or ErrNotFound. Envs
// are ambient (created out-of-band), so this is the lookup the env data source
// uses to turn a human-known name into the id resources scope against.
func (c *Client) FindEnv(ctx context.Context, orgID, name string) (*console.Harbor, error) {
	envs, err := c.ListHarbors(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return find(envs, func(h *console.Harbor) bool {
		return h.Slug == name || h.Name == name
	})
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

// CreateBucket places the bucket in zoneID, or the org's primary zone when
// zoneID is empty (the API resolves an omitted zone_id to "default").
func (c *Client) CreateBucket(ctx context.Context, harborID, name, zoneID string) error {
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return err
	}
	body := console.CreateHFBucketRequest{Name: name}
	if zoneID != "" {
		body.ZoneId = &zoneID
	}
	resp, err := c.api.CreateHarborBucketWithResponse(ctx, harbor, body)
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

// ── Bucket credentials (data source) ──────────────────────────────────────

// GetBucketCredentials returns the derived S3 credentials (access key, secret
// key, endpoint) for a bucket. The console mints these from the environment's
// object-storage owner; they are sensitive, so the data source that surfaces
// them keeps the secret out of logs and callers must treat state as secret.
func (c *Client) GetBucketCredentials(ctx context.Context, harborID, bucketName string) (*console.BucketCredentials, error) {
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetBucketCredentialsWithResponse(ctx, harbor, bucketName)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get bucket credentials", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get bucket credentials %q: empty response", bucketName)
	}
	return resp.JSON200, nil
}

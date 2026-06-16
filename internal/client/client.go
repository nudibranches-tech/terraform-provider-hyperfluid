// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ErrNotFound is returned when the API responds 404. Resources use it for drift
// detection (Read → remove from state) and delete confirmation (poll GET until
// ErrNotFound).
var ErrNotFound = errors.New("hyperfluid: resource not found")

// Client is a thin authenticated HTTP wrapper around the Console external API.
type Client struct {
	http    *http.Client
	baseURL string
}

// do performs a JSON request. A 404 maps to ErrNotFound; any other status >=400
// maps to an error carrying the response body. out may be nil (e.g. DELETE).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("hyperfluid: %s %s -> %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// ── Harbor (data source) ──────────────────────────────────────────────────

type Harbor struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (c *Client) ListHarbors(ctx context.Context, orgID string) ([]Harbor, error) {
	var out []Harbor
	p := fmt.Sprintf("/api/v1/organizations/%s/harbors", url.PathEscape(orgID))
	if err := c.do(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── Bucket (resource) ─────────────────────────────────────────────────────

type Bucket struct {
	Name         string `json:"name"`
	QuotaGB      *int64 `json:"quota_gb,omitempty"`
	FreezeWrites *bool  `json:"freeze_writes,omitempty"`
	Ready        bool   `json:"ready"`
}

// CreateBucketBody is the create payload — per the spec, create only accepts name.
type CreateBucketBody struct {
	Name string `json:"name"`
}

// PatchBucketBody carries the in-place-updatable (patch-only) fields.
type PatchBucketBody struct {
	QuotaGB      *int64 `json:"quota_gb,omitempty"`
	FreezeWrites *bool  `json:"freeze_writes,omitempty"`
}

func (c *Client) CreateBucket(ctx context.Context, harborID string, body CreateBucketBody) (*Bucket, error) {
	var out Bucket
	p := fmt.Sprintf("/api/v1/harbors/%s/buckets", url.PathEscape(harborID))
	if err := c.do(ctx, http.MethodPost, p, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetBucket(ctx context.Context, harborID, name string) (*Bucket, error) {
	var out Bucket
	p := fmt.Sprintf("/api/v1/harbors/%s/buckets/%s", url.PathEscape(harborID), url.PathEscape(name))
	if err := c.do(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PatchBucket(ctx context.Context, harborID, name string, body PatchBucketBody) (*Bucket, error) {
	var out Bucket
	p := fmt.Sprintf("/api/v1/harbors/%s/buckets/%s", url.PathEscape(harborID), url.PathEscape(name))
	if err := c.do(ctx, http.MethodPatch, p, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteBucket(ctx context.Context, harborID, name string) error {
	p := fmt.Sprintf("/api/v1/harbors/%s/buckets/%s", url.PathEscape(harborID), url.PathEscape(name))
	return c.do(ctx, http.MethodDelete, p, nil, nil)
}

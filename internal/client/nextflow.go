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

// ── Nextflow environment ───────────────────────────────────────────────────

func (c *Client) CreateNextflow(ctx context.Context, orgID, harborID string, body console.CreateNextflowCrdRequestBody) (*console.NextflowResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateNextflowCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create nextflow", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create nextflow: empty response")
	}
	return resp.JSON201, nil
}

// GetNextflow reads the DB projection: identity, description, tags and the
// cached phase.
func (c *Client) GetNextflow(ctx context.Context, orgID, nextflowID string) (*console.NextflowResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", nextflowID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetNextflowWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get nextflow", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get nextflow: empty response")
	}
	return resp.JSON200, nil
}

// GetNextflowCrd reads the live spec and the operator's status.
func (c *Client) GetNextflowCrd(ctx context.Context, orgID, nextflowID string) (*console.NextflowCrdResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", nextflowID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetNextflowCrdWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get nextflow spec", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get nextflow spec: empty response")
	}
	return resp.JSON200, nil
}

// FindNextflow resolves an environment's Nextflow deployment by name, or
// ErrNotFound.
func (c *Client) FindNextflow(ctx context.Context, orgID, harborID, name string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.ListNextflowsWithResponse(ctx, org, harbor)
	if err != nil {
		return "", err
	}
	if err := statusErr("list nextflows", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", ErrNotFound
	}
	n, err := findByName(*resp.JSON200, name, func(x *console.NextflowResponse) string { return x.Name })
	if err != nil {
		return "", err
	}
	return n.Id.String(), nil
}

// PatchNextflow sends body, with each field named in nulls set to JSON null:
// the generated type omits a nil field, and only an explicit null clears scm.
func (c *Client) PatchNextflow(ctx context.Context, orgID, nextflowID string, body console.PatchNextflowCrdRequestBody, nulls ...string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	id, err := parseUUID("id", nextflowID)
	if err != nil {
		return err
	}
	raw, err := withNulls(body, nulls)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchNextflowCrdWithBodyWithResponse(ctx, org, id, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return statusErr("patch nextflow", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteNextflow(ctx context.Context, orgID, nextflowID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	id, err := parseUUID("id", nextflowID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteNextflowCrdWithResponse(ctx, org, id)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete nextflow", resp.StatusCode(), resp.Body)
}

// withNulls marshals body and sets each named top-level field to null.
func withNulls(body any, fields []string) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil || len(fields) == 0 {
		return raw, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for _, f := range fields {
		m[f] = json.RawMessage("null")
	}
	return json.Marshal(m)
}

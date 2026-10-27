// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Compute Dock (Slurm cluster) ───────────────────────────────────────────

func (c *Client) CreateComputeDock(ctx context.Context, orgID, harborID string, body console.CreateComputeDockCrdRequestBody) (*console.ComputeDockResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateComputeDockCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create compute dock", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create compute dock: empty response")
	}
	return resp.JSON201, nil
}

// GetComputeDock reads the DB projection: identity, tags, endpoints and the
// phase and node counts the operator mirrors.
func (c *Client) GetComputeDock(ctx context.Context, orgID, dockID string) (*console.ComputeDockResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", dockID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetComputeDockWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get compute dock", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get compute dock: empty response")
	}
	return resp.JSON200, nil
}

// GetComputeDockSpec reads the live CRD spec.
func (c *Client) GetComputeDockSpec(ctx context.Context, orgID, dockID string) (*console.ComputeDockCrdSpecResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", dockID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetComputeDockCrdWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get compute dock spec", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get compute dock spec: empty response")
	}
	return resp.JSON200, nil
}

// FindComputeDock resolves a dock's id by name within an environment, or
// ErrNotFound.
func (c *Client) FindComputeDock(ctx context.Context, orgID, harborID, name string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.ListComputeDocksWithResponse(ctx, org, harbor)
	if err != nil {
		return "", err
	}
	if err := statusErr("list compute docks", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", ErrNotFound
	}
	d, err := findByName(*resp.JSON200, name, func(x *console.ComputeDockResponse) string { return x.Name })
	if err != nil {
		return "", err
	}
	return d.Id.String(), nil
}

// PatchComputeDock returns the partitions the patch dropped from the spec;
// their nodes keep running until the operator prunes them.
func (c *Client) PatchComputeDock(ctx context.Context, orgID, dockID string, body console.PatchComputeDockCrdRequestBody) ([]string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", dockID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.PatchComputeDockCrdWithResponse(ctx, org, id, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("patch compute dock", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return resp.JSON200.OrphanedPartitions, nil
}

func (c *Client) DeleteComputeDock(ctx context.Context, orgID, dockID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	id, err := parseUUID("id", dockID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteComputeDockCrdWithResponse(ctx, org, id)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete compute dock", resp.StatusCode(), resp.Body)
}

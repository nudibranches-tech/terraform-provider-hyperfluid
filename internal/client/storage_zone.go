// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Storage zone (data source) ────────────────────────────────────────────

// ListStorageZones returns the org's storage zones (the `cephZones` catalog
// joined with whether the org has enabled each one).
func (c *Client) ListStorageZones(ctx context.Context, orgID string) ([]console.StorageZoneView, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListOrgStorageZonesWithResponse(ctx, org)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list storage zones", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: list storage zones: empty response")
	}
	return *resp.JSON200, nil
}

// FindStorageZone resolves a storage zone by its zone id (the stable catalog
// key), or ErrNotFound. Zones are ambient (defined in the cluster's cephZones
// catalog), so this is the lookup the data source uses.
func (c *Client) FindStorageZone(ctx context.Context, orgID, zoneID string) (*console.StorageZoneView, error) {
	zones, err := c.ListStorageZones(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return find(zones, func(z *console.StorageZoneView) bool {
		return z.ZoneId == zoneID
	})
}

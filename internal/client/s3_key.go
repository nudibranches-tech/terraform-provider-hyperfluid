// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// S3 access keys are long-lived credentials minted by an environment's S3
// gateway for a service account. The secret is returned once, at issue; the
// key ledger is per (environment, storage zone), and zoneID "" is the
// environment's default zone.

func zoneParam(zoneID string) *string {
	if zoneID == "" {
		return nil
	}
	return &zoneID
}

func (c *Client) ListS3Keys(ctx context.Context, harborID, zoneID string) ([]console.S3KeyListEntry, error) {
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListS3KeysWithResponse(ctx, harbor, &console.ListS3KeysParams{ZoneId: zoneParam(zoneID)})
	if err != nil {
		return nil, err
	}
	if err := statusErr("list S3 keys", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// GetS3Key returns a key's ledger entry by id, or ErrNotFound (no GET-by-id
// route, so it lists and matches). A revoked key is still listed, with
// active = false.
func (c *Client) GetS3Key(ctx context.Context, harborID, zoneID, keyID string) (*console.S3KeyListEntry, error) {
	keys, err := c.ListS3Keys(ctx, harborID, zoneID)
	if err != nil {
		return nil, err
	}
	return find(keys, func(k *console.S3KeyListEntry) bool { return k.Id.String() == keyID })
}

// IssueS3Key mints a key for an existing service account.
func (c *Client) IssueS3Key(ctx context.Context, harborID string, body console.IssueS3KeyRequest) (*console.IssuedS3Key, error) {
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.IssueS3KeyWithResponse(ctx, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("issue S3 key", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: issue S3 key: empty response")
	}
	return resp.JSON200, nil
}

// RevokeS3Key revokes a key, and with it every older key of the same service
// account in the same zone. ErrNotFound means the key is unknown or already
// revoked; the API does not tell the two apart.
func (c *Client) RevokeS3Key(ctx context.Context, harborID, zoneID, keyID string) (*console.RevokedS3Key, error) {
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	key, err := parseUUID("key_id", keyID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.RevokeS3KeyWithResponse(ctx, harbor, key, &console.RevokeS3KeyParams{ZoneId: zoneParam(zoneID)})
	if err != nil {
		return nil, err
	}
	if err := statusErr("revoke S3 key", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: revoke S3 key: empty response")
	}
	return resp.JSON200, nil
}

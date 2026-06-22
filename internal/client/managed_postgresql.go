// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Managed PostgreSQL cluster ─────────────────────────────────────────────

func (c *Client) CreateManagedPostgresql(ctx context.Context, orgID, harborID string, body console.CreateManagedPostgresqlCrdRequestBody) (*console.ManagedPostgresqlResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateManagedPostgresqlCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create managed postgresql", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create managed postgresql: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetManagedPostgresql(ctx context.Context, orgID, instanceID string) (*console.ManagedPostgresqlResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetManagedPostgresqlWithResponse(ctx, org, inst)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get managed postgresql", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get managed postgresql: empty response")
	}
	return resp.JSON200, nil
}

// FindManagedPostgresql resolves a cluster's id by name within an environment,
// or ErrNotFound. Backs the data source.
func (c *Client) FindManagedPostgresql(ctx context.Context, orgID, harborID, name string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.ListManagedPostgresqlsWithResponse(ctx, org, harbor)
	if err != nil {
		return "", err
	}
	if err := statusErr("list managed postgresqls", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", ErrNotFound
	}
	m, err := findByName(*resp.JSON200, name, func(x *console.ManagedPostgresqlResponse) string {
		return x.Name
	})
	if err != nil {
		return "", err
	}
	return m.Id.String(), nil
}

func (c *Client) PatchManagedPostgresql(ctx context.Context, orgID, instanceID string, body console.PatchManagedPostgresqlCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchManagedPostgresqlCrdWithResponse(ctx, org, inst, body)
	if err != nil {
		return err
	}
	return statusErr("patch managed postgresql", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteManagedPostgresql(ctx context.Context, orgID, instanceID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteManagedPostgresqlCrdWithResponse(ctx, org, inst)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete managed postgresql", resp.StatusCode(), resp.Body)
}

// ── Managed PostgreSQL user (sub-resource of a cluster) ────────────────────

func (c *Client) CreateManagedPostgresqlUser(ctx context.Context, orgID, clusterID string, body console.CreateManagedPostgresqlUserCrdRequestBody) (*console.ManagedPostgresqlUserResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	cluster, err := parseUUID("managed_postgresql", clusterID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateManagedPostgresqlUserCrdWithResponse(ctx, org, cluster, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create managed postgresql user", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create managed postgresql user: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetManagedPostgresqlUser(ctx context.Context, orgID, clusterID, userID string) (*console.ManagedPostgresqlUserResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	cluster, err := parseUUID("managed_postgresql", clusterID)
	if err != nil {
		return nil, err
	}
	user, err := parseUUID("id", userID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetManagedPostgresqlUserWithResponse(ctx, org, cluster, user)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get managed postgresql user", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get managed postgresql user: empty response")
	}
	return resp.JSON200, nil
}

// FindManagedPostgresqlUser resolves a user by username within a cluster, or
// ErrNotFound. Returns the object (the data source's toModel needs it).
func (c *Client) FindManagedPostgresqlUser(ctx context.Context, orgID, clusterID, username string) (*console.ManagedPostgresqlUserResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	cluster, err := parseUUID("managed_postgresql", clusterID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListManagedPostgresqlUsersWithResponse(ctx, org, cluster)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list managed postgresql users", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, ErrNotFound
	}
	return findByName(*resp.JSON200, username, func(u *console.ManagedPostgresqlUserResponse) string {
		return u.Username
	})
}

func (c *Client) PatchManagedPostgresqlUser(ctx context.Context, orgID, clusterID, userID string, body console.PatchManagedPostgresqlUserCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	cluster, err := parseUUID("managed_postgresql", clusterID)
	if err != nil {
		return err
	}
	user, err := parseUUID("id", userID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchManagedPostgresqlUserCrdWithResponse(ctx, org, cluster, user, body)
	if err != nil {
		return err
	}
	return statusErr("patch managed postgresql user", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteManagedPostgresqlUser(ctx context.Context, orgID, clusterID, userID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	cluster, err := parseUUID("managed_postgresql", clusterID)
	if err != nil {
		return err
	}
	user, err := parseUUID("id", userID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteManagedPostgresqlUserCrdWithResponse(ctx, org, cluster, user)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete managed postgresql user", resp.StatusCode(), resp.Body)
}

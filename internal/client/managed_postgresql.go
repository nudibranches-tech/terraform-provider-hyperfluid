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

// ── Managed PostgreSQL cluster ─────────────────────────────────────────────

func (c *Client) CreateManagedPostgresql(ctx context.Context, orgID, harborID string, body console.CreateManagedPostgresqlCrdRequestBody) (*console.ManagedPostgresqlResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("environment", harborID)
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

// GetManagedPostgresqlSpec returns the desired CRD spec (the read-mapping source
// for spec-only fields like expose_to_internet that the status view does not echo).
// Only the /crd GET carries them.
func (c *Client) GetManagedPostgresqlSpec(ctx context.Context, orgID, instanceID string) (*console.ManagedPostgresqlCrdSpecResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetManagedPostgresqlCrdWithResponse(ctx, org, inst)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get managed postgresql spec", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get managed postgresql spec: empty response")
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
	harbor, err := parseUUID("environment", harborID)
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

// PatchManagedPostgresqlOptions says what a PATCH body cannot: the generated
// type drops a nil field, and the API reads an absent field and a `null` one
// differently.
type PatchManagedPostgresqlOptions struct {
	// ClearArchiveInterval sends `pitr.archive_interval_seconds: null`, which
	// clears a pinned interval back to the platform's recommended one. An
	// absent interval keeps whatever the database pins. Needs body.Pitr.
	ClearArchiveInterval bool
}

func (c *Client) PatchManagedPostgresql(ctx context.Context, orgID, instanceID string, body console.PatchManagedPostgresqlCrdRequestBody, opts PatchManagedPostgresqlOptions) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return err
	}
	raw, err := patchManagedPostgresqlBody(body, opts)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchManagedPostgresqlCrdWithBodyWithResponse(ctx, org, inst, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return statusErr("patch managed postgresql", resp.StatusCode(), resp.Body)
}

// patchManagedPostgresqlBody marshals body, then writes the explicit nulls
// opts asks for.
func patchManagedPostgresqlBody(body console.PatchManagedPostgresqlCrdRequestBody, opts PatchManagedPostgresqlOptions) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if !opts.ClearArchiveInterval {
		return raw, nil
	}
	if body.Pitr == nil {
		return nil, fmt.Errorf("hyperfluid: patch managed postgresql: clearing the archive interval needs a pitr block")
	}
	if body.Pitr.ArchiveIntervalSeconds != nil {
		return nil, fmt.Errorf("hyperfluid: patch managed postgresql: archive interval both set and cleared")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var pitr map[string]json.RawMessage
	if err := json.Unmarshal(fields["pitr"], &pitr); err != nil {
		return nil, err
	}
	pitr["archive_interval_seconds"] = json.RawMessage("null")
	if fields["pitr"], err = json.Marshal(pitr); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
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

// ── Managed PostgreSQL extensions ──────────────────────────────────────────

// GetManagedPostgresqlExtensions returns the platform catalog as it applies to
// one cluster, each entry joined with what was declared for it and what the
// operator last observed.
func (c *Client) GetManagedPostgresqlExtensions(ctx context.Context, orgID, instanceID string) (*console.ManagedPostgresqlExtensionsResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetManagedPostgresqlExtensionsWithResponse(ctx, org, inst)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get managed postgresql extensions", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get managed postgresql extensions: empty response")
	}
	return resp.JSON200, nil
}

// SetManagedPostgresqlExtensions replaces a cluster's declared extension list.
// An installed extension left out of it stays installed (reported `unmanaged`);
// dropping one takes an explicit `ensure: absent` entry.
func (c *Client) SetManagedPostgresqlExtensions(ctx context.Context, orgID, instanceID string, extensions []console.ExtensionRequestItem) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return err
	}
	if extensions == nil {
		extensions = []console.ExtensionRequestItem{}
	}
	resp, err := c.api.SetManagedPostgresqlExtensionsWithResponse(ctx, org, inst,
		console.SetManagedPostgresqlExtensionsRequestBody{Extensions: extensions})
	if err != nil {
		return err
	}
	return statusErr("set managed postgresql extensions", resp.StatusCode(), resp.Body)
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

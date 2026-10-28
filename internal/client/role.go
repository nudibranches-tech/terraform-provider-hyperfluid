// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// Roles are organization-scoped permission bundles. The system roles (owner,
// editor, viewer) exist in every organization and refuse updates and deletes.

// ListRoles lists the organization's roles whose name contains name
// (case-insensitive), or all of them when name is empty.
func (c *Client) ListRoles(ctx context.Context, orgID, name string) ([]console.Role, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	params := &console.ListRolesParams{OrganizationId: org}
	if name != "" {
		params.Name = &name
	}
	resp, err := c.api.ListRolesWithResponse(ctx, params)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list roles", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return resp.JSON200.Roles, nil
}

// FindRoleByName resolves a role by its name, unique within the organization.
// An exact match wins; otherwise a single case-insensitive match is accepted,
// since the platform compares system role names that way.
func (c *Client) FindRoleByName(ctx context.Context, orgID, name string) (*console.Role, error) {
	roles, err := c.ListRoles(ctx, orgID, name)
	if err != nil {
		return nil, err
	}
	if r, err := findByName(roles, name, func(r *console.Role) string { return r.Name }); err == nil {
		return r, nil
	}
	var matches []*console.Role
	for i := range roles {
		if strings.EqualFold(roles[i].Name, name) {
			matches = append(matches, &roles[i])
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("hyperfluid: %d roles named %q differing only in case; use the exact name", len(matches), name)
	}
}

func (c *Client) GetRole(ctx context.Context, roleID string) (*console.Role, error) {
	role, err := parseUUID("role_id", roleID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetRoleWithResponse(ctx, role)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get role", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get role: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) CreateRole(ctx context.Context, orgID string, body console.CreateRoleRequestBody) (*console.Role, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	body.OrganizationId = org
	resp, err := c.api.CreateRoleWithResponse(ctx, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create role", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create role: empty response")
	}
	return resp.JSON201, nil
}

// UpdateRole replaces the role's name, color and description; an omitted
// color or description is cleared.
func (c *Client) UpdateRole(ctx context.Context, roleID string, body console.UpdateRoleRequestBody) (*console.Role, error) {
	role, err := parseUUID("role_id", roleID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.UpdateRoleWithResponse(ctx, role, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("update role", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: update role: empty response")
	}
	return resp.JSON200, nil
}

// DeleteRole deletes the role, its permission bundle and every binding of it.
// A role already gone is not an error.
func (c *Client) DeleteRole(ctx context.Context, roleID string) error {
	role, err := parseUUID("role_id", roleID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteRoleWithResponse(ctx, role)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete role", resp.StatusCode(), resp.Body)
}

// ListUserRoles lists the roles a user holds organization-wide.
func (c *Client) ListUserRoles(ctx context.Context, orgID, userID string) ([]console.Role, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	user, err := parseUUID("user_id", userID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListUserRolesWithResponse(ctx, org, user)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list user roles", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// Principal types a grant or a role binding is keyed on. Group members are
// users or service accounts; grants and role bindings also take a group.
const (
	PrincipalUser           = "user"
	PrincipalServiceAccount = "service_account"
	PrincipalGroup          = "group"
)

// needs names the permission a route requires when its 403 does not: the
// authorization-management routes refuse with a bare "You are not authorized
// ..." that leaves the caller guessing what to ask an administrator for.
func needs(err error, permission string) error {
	if errors.Is(err, ErrForbidden) && strings.Contains(err.Error(), "You are not authorized") {
		return fmt.Errorf("%w (this call requires the %q permission)", err, permission)
	}
	return err
}

// ── Permission catalog and role bundles ──────────────────────────────────

func (c *Client) ListPermissions(ctx context.Context) ([]console.PermissionCatalogEntry, error) {
	resp, err := c.api.ListPermissionsWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list permissions", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

func (c *Client) ListRolePermissions(ctx context.Context, roleID string) ([]console.PermissionCatalogEntry, error) {
	role, err := parseUUID("role_id", roleID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListRolePermissionsWithResponse(ctx, role)
	if err != nil {
		return nil, err
	}
	if err := needs(statusErr("list role permissions", resp.StatusCode(), resp.Body), "role:read"); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// SetRolePermissions replaces the role's whole permission bundle; an empty
// list empties it.
func (c *Client) SetRolePermissions(ctx context.Context, roleID string, permissionIDs []openapi_types.UUID) error {
	role, err := parseUUID("role_id", roleID)
	if err != nil {
		return err
	}
	if permissionIDs == nil {
		permissionIDs = []openapi_types.UUID{}
	}
	resp, err := c.api.SetRolePermissionsWithResponse(ctx, role, console.SetRolePermissionsRequestBody{PermissionIds: permissionIDs})
	if err != nil {
		return err
	}
	return needs(statusErr("set role permissions", resp.StatusCode(), resp.Body), "role:update")
}

// ── Groups ────────────────────────────────────────────────────────────────

func (c *Client) ListGroups(ctx context.Context, orgID string) ([]console.Group, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListGroupsWithResponse(ctx, org)
	if err != nil {
		return nil, err
	}
	if err := needs(statusErr("list groups", resp.StatusCode(), resp.Body), "group:read"); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// FindGroupByName resolves a group by its name, unique within the organization.
func (c *Client) FindGroupByName(ctx context.Context, orgID, name string) (*console.Group, error) {
	groups, err := c.ListGroups(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return findByName(groups, name, func(g *console.Group) string { return g.Name })
}

func (c *Client) GetGroup(ctx context.Context, orgID, groupID string) (*console.Group, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	group, err := parseUUID("group_id", groupID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetGroupWithResponse(ctx, org, group)
	if err != nil {
		return nil, err
	}
	if err := needs(statusErr("get group", resp.StatusCode(), resp.Body), "group:read"); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get group: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) CreateGroup(ctx context.Context, orgID string, body console.CreateGroupRequestBody) (*console.Group, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateGroupWithResponse(ctx, org, body)
	if err != nil {
		return nil, err
	}
	if err := needs(statusErr("create group", resp.StatusCode(), resp.Body), "group:create"); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create group: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) UpdateGroup(ctx context.Context, orgID, groupID string, body console.UpdateGroupRequestBody) (*console.Group, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	group, err := parseUUID("group_id", groupID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.UpdateGroupWithResponse(ctx, org, group, body)
	if err != nil {
		return nil, err
	}
	if err := needs(statusErr("update group", resp.StatusCode(), resp.Body), "group:update"); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: update group: empty response")
	}
	return resp.JSON200, nil
}

// DeleteGroup deletes the group and every grant bound to it. A group already
// gone is not an error.
func (c *Client) DeleteGroup(ctx context.Context, orgID, groupID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	group, err := parseUUID("group_id", groupID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteGroupWithResponse(ctx, org, group)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return needs(statusErr("delete group", resp.StatusCode(), resp.Body), "group:delete")
}

// ── Group members ─────────────────────────────────────────────────────────

// ListGroupMembers returns ErrNotFound when the group itself is gone.
func (c *Client) ListGroupMembers(ctx context.Context, orgID, groupID string) ([]console.GroupMember, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	group, err := parseUUID("group_id", groupID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListGroupMembersWithResponse(ctx, org, group)
	if err != nil {
		return nil, err
	}
	if err := needs(statusErr("list group members", resp.StatusCode(), resp.Body), "group:read"); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// GetGroupMember returns the member, or ErrNotFound when either the group or
// the membership is gone.
func (c *Client) GetGroupMember(ctx context.Context, orgID, groupID, principalType, principalID string) (*console.GroupMember, error) {
	members, err := c.ListGroupMembers(ctx, orgID, groupID)
	if err != nil {
		return nil, err
	}
	return find(members, func(m *console.GroupMember) bool {
		return m.PrincipalType == principalType && m.PrincipalId.String() == principalID
	})
}

// AddGroupMember is idempotent: adding a member twice is a no-op.
func (c *Client) AddGroupMember(ctx context.Context, orgID, groupID, principalType, principalID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	group, err := parseUUID("group_id", groupID)
	if err != nil {
		return err
	}
	principal, err := parseUUID("principal_id", principalID)
	if err != nil {
		return err
	}
	resp, err := c.api.AddGroupMemberWithResponse(ctx, org, group, console.AddGroupMemberRequestBody{
		PrincipalType: principalType,
		PrincipalId:   principal,
	})
	if err != nil {
		return err
	}
	return needs(statusErr("add group member", resp.StatusCode(), resp.Body), "group:update")
}

// RemoveGroupMember is idempotent once the group exists; a group already gone
// is not an error either.
func (c *Client) RemoveGroupMember(ctx context.Context, orgID, groupID, principalType, principalID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	group, err := parseUUID("group_id", groupID)
	if err != nil {
		return err
	}
	principal, err := parseUUID("principal_id", principalID)
	if err != nil {
		return err
	}
	resp, err := c.api.RemoveGroupMemberWithResponse(ctx, org, group, principalType, principal)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return needs(statusErr("remove group member", resp.StatusCode(), resp.Body), "group:update")
}

// ── Role bindings ─────────────────────────────────────────────────────────

// RoleScope narrows a role binding. Every field is optional: an empty scope
// binds the role organization-wide.
type RoleScope struct {
	ScopePath    string
	ResourceUUID string
	ResourceType string
}

func (s RoleScope) params() (scopePath *string, resourceUUID *openapi_types.UUID, resourceType *string, err error) {
	if s.ScopePath != "" {
		scopePath = &s.ScopePath
	}
	if s.ResourceUUID != "" {
		u, err := parseUUID("resource_uuid", s.ResourceUUID)
		if err != nil {
			return nil, nil, nil, err
		}
		resourceUUID = &u
	}
	if s.ResourceType != "" {
		resourceType = &s.ResourceType
	}
	return scopePath, resourceUUID, resourceType, nil
}

// BindRole binds a role to a group, a service account or a user. Binding the
// same role at the same scope twice is a no-op.
func (c *Client) BindRole(ctx context.Context, orgID, principalType, principalID, roleID string, scope RoleScope) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	principal, err := parseUUID("principal_id", principalID)
	if err != nil {
		return err
	}
	role, err := parseUUID("role_id", roleID)
	if err != nil {
		return err
	}
	scopePath, resourceUUID, resourceType, err := scope.params()
	if err != nil {
		return err
	}
	switch principalType {
	case PrincipalGroup:
		resp, err := c.api.BindRoleToGroupWithResponse(ctx, org, principal, role, &console.BindRoleToGroupParams{
			ScopePath: scopePath, ResourceUuid: resourceUUID, ResourceType: resourceType,
		})
		if err != nil {
			return err
		}
		return needs(statusErr("bind role to group", resp.StatusCode(), resp.Body), "grant:create")
	case PrincipalServiceAccount:
		resp, err := c.api.BindRoleToServiceAccountWithResponse(ctx, org, principal, role, &console.BindRoleToServiceAccountParams{
			ScopePath: scopePath, ResourceUuid: resourceUUID, ResourceType: resourceType,
		})
		if err != nil {
			return err
		}
		return needs(statusErr("bind role to service account", resp.StatusCode(), resp.Body), "service_account:update")
	case PrincipalUser:
		resp, err := c.api.AddRoleToUserWithResponse(ctx, org, principal, role, &console.AddRoleToUserParams{
			ScopePath: scopePath, ResourceUuid: resourceUUID, ResourceType: resourceType,
		})
		if err != nil {
			return err
		}
		return statusErr("bind role to user", resp.StatusCode(), resp.Body)
	default:
		return fmt.Errorf("hyperfluid: a role cannot be bound to a %q", principalType)
	}
}

// UnbindRole removes the binding of the role at exactly scope.ScopePath. A
// binding already gone is not an error.
func (c *Client) UnbindRole(ctx context.Context, orgID, principalType, principalID, roleID string, scope RoleScope) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	principal, err := parseUUID("principal_id", principalID)
	if err != nil {
		return err
	}
	role, err := parseUUID("role_id", roleID)
	if err != nil {
		return err
	}
	scopePath, resourceUUID, resourceType, err := scope.params()
	if err != nil {
		return err
	}
	var status int
	var body []byte
	var permission string
	switch principalType {
	case PrincipalGroup:
		resp, err := c.api.UnbindRoleFromGroupWithResponse(ctx, org, principal, role, &console.UnbindRoleFromGroupParams{
			ScopePath: scopePath, ResourceUuid: resourceUUID, ResourceType: resourceType,
		})
		if err != nil {
			return err
		}
		status, body, permission = resp.StatusCode(), resp.Body, "grant:delete"
	case PrincipalServiceAccount:
		resp, err := c.api.UnbindRoleFromServiceAccountWithResponse(ctx, org, principal, role, &console.UnbindRoleFromServiceAccountParams{
			ScopePath: scopePath, ResourceUuid: resourceUUID, ResourceType: resourceType,
		})
		if err != nil {
			return err
		}
		status, body, permission = resp.StatusCode(), resp.Body, "service_account:update"
	case PrincipalUser:
		resp, err := c.api.RemoveRoleFromUserWithResponse(ctx, org, principal, role, &console.RemoveRoleFromUserParams{
			ScopePath: scopePath,
		})
		if err != nil {
			return err
		}
		status, body, permission = resp.StatusCode(), resp.Body, "user:update"
	default:
		return fmt.Errorf("hyperfluid: a role cannot be bound to a %q", principalType)
	}
	// The principal or the role is gone, and the binding with it.
	if status == http.StatusNotFound {
		return nil
	}
	return needs(statusErr("unbind role", status, body), permission)
}

// FindRoleBinding returns the grant a role binding is stored as. Bindings have
// no id of their own: one is the principal's grant carrying role_id, at
// exactly that scope_path and resource_uuid.
func (c *Client) FindRoleBinding(ctx context.Context, orgID, principalType, principalID, roleID string, scope RoleScope) (*console.Grant, error) {
	grants, err := c.ListGrants(ctx, orgID, principalType, principalID)
	if err != nil {
		return nil, err
	}
	return find(grants, func(g *console.Grant) bool {
		return g.PermissionId == nil && g.RoleId != nil && g.RoleId.String() == roleID &&
			deref(g.ScopePath) == scope.ScopePath && uuidString(g.ResourceUuid) == scope.ResourceUUID
	})
}

// ── Grants ────────────────────────────────────────────────────────────────

// ListGrants lists the grants held directly by one principal, role bindings
// included. A principal that no longer exists holds none.
func (c *Client) ListGrants(ctx context.Context, orgID, principalType, principalID string) ([]console.Grant, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	principal, err := parseUUID("principal_id", principalID)
	if err != nil {
		return nil, err
	}
	var status int
	var body []byte
	var grants *[]console.Grant
	switch principalType {
	case PrincipalUser:
		resp, err := c.api.ListUserGrantsWithResponse(ctx, org, principal)
		if err != nil {
			return nil, err
		}
		status, body, grants = resp.StatusCode(), resp.Body, resp.JSON200
	case PrincipalServiceAccount:
		resp, err := c.api.ListServiceAccountGrantsWithResponse(ctx, org, principal)
		if err != nil {
			return nil, err
		}
		status, body, grants = resp.StatusCode(), resp.Body, resp.JSON200
	case PrincipalGroup:
		resp, err := c.api.ListGroupGrantsWithResponse(ctx, org, principal)
		if err != nil {
			return nil, err
		}
		status, body, grants = resp.StatusCode(), resp.Body, resp.JSON200
	default:
		return nil, fmt.Errorf("hyperfluid: unknown principal type %q", principalType)
	}
	if err := needs(statusErr("list grants", status, body), "grant:read"); err != nil {
		return nil, err
	}
	if grants == nil {
		return nil, nil
	}
	return *grants, nil
}

// GetGrant returns a grant by id, or ErrNotFound. There is no GET-by-id route,
// so it lists the principal's grants and matches.
func (c *Client) GetGrant(ctx context.Context, orgID, principalType, principalID, grantID string) (*console.Grant, error) {
	grants, err := c.ListGrants(ctx, orgID, principalType, principalID)
	if err != nil {
		return nil, err
	}
	return find(grants, func(g *console.Grant) bool { return g.Id.String() == grantID })
}

func (c *Client) CreateGrant(ctx context.Context, orgID, principalType, principalID string, body console.CreateGrantRequestBody) (*console.Grant, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	principal, err := parseUUID("principal_id", principalID)
	if err != nil {
		return nil, err
	}
	var status int
	var raw []byte
	var grant *console.Grant
	switch principalType {
	case PrincipalUser:
		resp, err := c.api.CreateUserGrantWithResponse(ctx, org, principal, body)
		if err != nil {
			return nil, err
		}
		status, raw, grant = resp.StatusCode(), resp.Body, resp.JSON201
	case PrincipalServiceAccount:
		resp, err := c.api.CreateServiceAccountGrantWithResponse(ctx, org, principal, body)
		if err != nil {
			return nil, err
		}
		status, raw, grant = resp.StatusCode(), resp.Body, resp.JSON201
	case PrincipalGroup:
		resp, err := c.api.CreateGroupGrantWithResponse(ctx, org, principal, body)
		if err != nil {
			return nil, err
		}
		status, raw, grant = resp.StatusCode(), resp.Body, resp.JSON201
	default:
		return nil, fmt.Errorf("hyperfluid: unknown principal type %q", principalType)
	}
	if err := needs(statusErr("create grant", status, raw), "grant:create"); err != nil {
		return nil, err
	}
	if grant == nil {
		return nil, fmt.Errorf("hyperfluid: create grant: empty response")
	}
	return grant, nil
}

// DeleteGrant deletes a grant. A grant already gone is not an error.
func (c *Client) DeleteGrant(ctx context.Context, orgID, grantID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	grant, err := parseUUID("grant_id", grantID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteGrantWithResponse(ctx, org, grant)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return needs(statusErr("delete grant", resp.StatusCode(), resp.Body), "grant:delete")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func uuidString(u *openapi_types.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}

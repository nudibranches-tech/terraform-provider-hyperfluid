// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// Service accounts are created in the console or with hfctl; the provider only
// looks them up and manages their attributes. Grants, roles and attributes take
// the console row's uuid.

// ListServiceAccounts lists the organization's accounts whose display name
// contains name (case-insensitive), or all of them when name is empty. The API
// returns at most the 100 most recently updated.
func (c *Client) ListServiceAccounts(ctx context.Context, orgID, name string) ([]console.ServiceAccount, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	params := &console.ListServiceAccountsHandlerParams{}
	if name != "" {
		params.Name = &name
	}
	resp, err := c.api.ListServiceAccountsHandlerWithResponse(ctx, org, params)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list service accounts", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return resp.JSON200.ServiceAccounts, nil
}

// FindServiceAccount resolves the console row of the account with this
// client_id, or ErrNotFound. There is no lookup by client_id, so it lists,
// which only reaches the 100 most recently updated accounts.
func (c *Client) FindServiceAccount(ctx context.Context, orgID, clientID string) (*console.ServiceAccount, error) {
	accounts, err := c.ListServiceAccounts(ctx, orgID, "")
	if err != nil {
		return nil, err
	}
	return find(accounts, func(sa *console.ServiceAccount) bool { return sa.IamSaClientId == clientID })
}

// FindServiceAccountByName resolves an account by its exact display name.
// Display names are not unique, so more than one match is an error rather than
// an arbitrary pick.
func (c *Client) FindServiceAccountByName(ctx context.Context, orgID, name string) (*console.ServiceAccount, error) {
	accounts, err := c.ListServiceAccounts(ctx, orgID, name)
	if err != nil {
		return nil, err
	}
	var matches []*console.ServiceAccount
	for i := range accounts {
		if accounts[i].Name == name {
			matches = append(matches, &accounts[i])
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("hyperfluid: %d service accounts named %q; look the account up by client_id", len(matches), name)
	}
}

// GetServiceAccountAttributes returns the account's `namespace::value`
// attributes, or ErrNotFound when the account is gone.
func (c *Client) GetServiceAccountAttributes(ctx context.Context, orgID, serviceAccountID string) ([]string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	sa, err := parseUUID("service_account_id", serviceAccountID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetServiceAccountAttributesHandlerWithResponse(ctx, org, sa)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get service account attributes", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return resp.JSON200.Attributes, nil
}

// SetServiceAccountAttributes replaces the account's whole attribute set; an
// empty list clears it.
func (c *Client) SetServiceAccountAttributes(ctx context.Context, orgID, serviceAccountID string, attributes []string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	sa, err := parseUUID("service_account_id", serviceAccountID)
	if err != nil {
		return err
	}
	if attributes == nil {
		attributes = []string{}
	}
	resp, err := c.api.SetServiceAccountAttributesHandlerWithResponse(ctx, org, sa, console.SetServiceAccountAttributesRequest{Attributes: attributes})
	if err != nil {
		return err
	}
	return statusErr("set service account attributes", resp.StatusCode(), resp.Body)
}

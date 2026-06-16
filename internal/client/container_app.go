// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// Container apps are addressed by app id (uuid) for get/patch/delete, but
// created under a harbor and the create call returns no body — so the id is
// discovered by listing the harbor's apps and matching on name.

func (c *Client) CreateContainerApp(ctx context.Context, orgID, harborID string, body console.CreateContainerAppCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return err
	}
	resp, err := c.api.CreateContainerAppCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return err
	}
	return statusErr("create container app", resp.StatusCode(), resp.Body)
}

// FindContainerAppID returns the id of the app named name in the given harbor,
// or ErrNotFound if absent.
func (c *Client) FindContainerAppID(ctx context.Context, orgID, harborID, name string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.ListContainerAppsWithResponse(ctx, org, harbor)
	if err != nil {
		return "", err
	}
	if err := statusErr("list container apps", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 != nil {
		for _, a := range *resp.JSON200 {
			if a.Name == name {
				return a.Id.String(), nil
			}
		}
	}
	return "", ErrNotFound
}

// GetContainerAppStatus returns the runtime view (phase, replica counts,
// endpoint) — used for wait-for-ready and computed status attributes.
func (c *Client) GetContainerAppStatus(ctx context.Context, orgID, appID string) (*console.ContainerAppResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	app, err := parseUUID("id", appID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetContainerAppWithResponse(ctx, org, app)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get container app status", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get container app status: empty response")
	}
	return resp.JSON200, nil
}

// GetContainerAppSpec returns the desired spec (image, env, resource_version,
// tier-expanded cpu/mem) — the read-mapping source. Only the /crd GET carries it.
func (c *Client) GetContainerAppSpec(ctx context.Context, orgID, appID string) (*console.ContainerAppCrdSpecResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	app, err := parseUUID("id", appID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetContainerAppCrdWithResponse(ctx, org, app)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get container app spec", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get container app spec: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) PatchContainerApp(ctx context.Context, orgID, appID string, body console.PatchContainerAppCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	app, err := parseUUID("id", appID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchContainerAppCrdWithResponse(ctx, org, app, body)
	if err != nil {
		return err
	}
	return statusErr("patch container app", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteContainerApp(ctx context.Context, orgID, appID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	app, err := parseUUID("id", appID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteContainerAppCrdWithResponse(ctx, org, app)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete container app", resp.StatusCode(), resp.Body)
}

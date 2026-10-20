// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// Container apps are addressed by app id (uuid) for get/patch/delete, but
// created under a harbor.

// CreateContainerApp creates an app and returns its id. The console answers the
// create with the new app's id, which the spec does not declare; a console that
// sends none is older than display names, so the slug it was sent as `name`
// finds the app.
func (c *Client) CreateContainerApp(ctx context.Context, orgID, harborID string, body console.CreateContainerAppCrdRequestBody) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.CreateContainerAppCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return "", err
	}
	if err := statusErr("create container app", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(resp.Body, &created) == nil && created.ID != "" {
		return created.ID, nil
	}
	slug := body.Name
	if body.Slug != nil {
		slug = *body.Slug
	}
	return c.FindContainerAppIDBySlug(ctx, orgID, harborID, slug)
}

// FindContainerAppIDBySlug returns the id of the app with the given slug in the
// given harbor, or ErrNotFound if absent. The slug is an app's only unique key:
// several apps in a harbor may share a display name.
func (c *Client) FindContainerAppIDBySlug(ctx context.Context, orgID, harborID, slug string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("environment", harborID)
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
	if resp.JSON200 == nil {
		return "", ErrNotFound
	}
	app, err := findByName(*resp.JSON200, slug, func(a *console.ContainerAppResponse) string {
		return a.Slug
	})
	if err != nil {
		return "", err
	}
	return app.Id.String(), nil
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

// PatchContainerAppMetadata updates an app's display name, description and
// tags. They live outside the app's spec, so this takes no resource_version.
func (c *Client) PatchContainerAppMetadata(ctx context.Context, orgID, appID string, body console.PatchContainerAppRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	app, err := parseUUID("id", appID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchContainerAppWithResponse(ctx, org, app, body)
	if err != nil {
		return err
	}
	return statusErr("update container app metadata", resp.StatusCode(), resp.Body)
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

// DetachImageSource removes an app's Git source. The app keeps running the image
// the source last resolved, which becomes its literal image.
func (c *Client) DetachImageSource(ctx context.Context, orgID, appID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	app, err := parseUUID("id", appID)
	if err != nil {
		return err
	}
	resp, err := c.api.DetachImageSourceWithResponse(ctx, org, app, console.DetachImageSourceJSONRequestBody{})
	if err != nil {
		return err
	}
	return statusErr("detach image source", resp.StatusCode(), resp.Body)
}

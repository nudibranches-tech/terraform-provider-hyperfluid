// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── SQL engine cluster (the API's data dock) ───────────────────────────────

func (c *Client) CreateDataDock(ctx context.Context, body console.CreateDataDockRequestBody) (*console.DataDockExternalResponseData, error) {
	resp, err := c.api.CreateDataDockCrdWithResponse(ctx, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create sql engine cluster", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create sql engine cluster: empty response")
	}
	return resp.JSON201, nil
}

// GetDataDock finds a cluster by id in the organization's list, or ErrNotFound:
// the external API has no read by id.
func (c *Client) GetDataDock(ctx context.Context, orgID, id string) (*console.DataDock, error) {
	docks, err := c.listDataDocks(ctx, orgID, nil)
	if err != nil {
		return nil, err
	}
	return find(docks, func(d *console.DataDock) bool { return d.Id.String() == id })
}

// FindDataDock resolves a cluster by its name within an environment, or ErrNotFound.
func (c *Client) FindDataDock(ctx context.Context, orgID, harborID, name string) (*console.DataDock, error) {
	docks, err := c.listDataDocks(ctx, orgID, &name)
	if err != nil {
		return nil, err
	}
	return find(docks, func(d *console.DataDock) bool { return d.HarborId.String() == harborID && d.Slug == name })
}

func (c *Client) listDataDocks(ctx context.Context, orgID string, slug *string) ([]console.DataDock, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListDataDocksWithResponse(ctx, org, &console.ListDataDocksParams{Slug: slug})
	if err != nil {
		return nil, err
	}
	if err := statusErr("list sql engine clusters", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: list sql engine clusters: empty response")
	}
	return *resp.JSON200, nil
}

func (c *Client) UpdateDataDock(ctx context.Context, id string, body console.UpdateDataDockRequest) error {
	dock, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.UpdateDataDockWithResponse(ctx, dock, body)
	if err != nil {
		return err
	}
	return statusErr("update sql engine cluster", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteDataDock(ctx context.Context, id string) error {
	dock, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteDataDockCrdWithResponse(ctx, dock)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete sql engine cluster", resp.StatusCode(), resp.Body)
}

// GetDataDockExposure returns the exposure as written in the spec: nil when it
// was never set.
func (c *Client) GetDataDockExposure(ctx context.Context, id string) (*bool, error) {
	dock, err := parseUUID("id", id)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetDataDockInternetExposureWithResponse(ctx, dock)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get sql engine cluster exposure", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get sql engine cluster exposure: empty response")
	}
	return resp.JSON200.ExposeToInternet, nil
}

func (c *Client) SetDataDockExposure(ctx context.Context, id string, exposed bool) error {
	dock, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.SetDataDockInternetExposureWithResponse(ctx, dock,
		console.UpdateDataDockInternetExposureRequestBody{ExposeToInternet: exposed})
	if err != nil {
		return err
	}
	return statusErr("set sql engine cluster exposure", resp.StatusCode(), resp.Body)
}

// ── SQL engine catalog ─────────────────────────────────────────────────────

func (c *Client) CreateDataDockCatalog(ctx context.Context, dockID string, body console.CreateDataDockCatalogRequest) error {
	dock, err := parseUUID("cluster_id", dockID)
	if err != nil {
		return err
	}
	resp, err := c.api.CreateDataDockCatalogWithResponse(ctx, dock, body)
	if err != nil {
		return err
	}
	return statusErr("create sql engine catalog", resp.StatusCode(), resp.Body)
}

// GetDataDockCatalog finds a catalog by name in its cluster's list, or
// ErrNotFound (also when the cluster itself is gone).
func (c *Client) GetDataDockCatalog(ctx context.Context, dockID, name string) (*console.DataDockCatalogInfo, error) {
	dock, err := parseUUID("cluster_id", dockID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListDataDockCatalogsWithResponse(ctx, dock)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list sql engine catalogs", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, ErrNotFound
	}
	return findByName(*resp.JSON200, name, func(i *console.DataDockCatalogInfo) string { return i.Name })
}

func (c *Client) DeleteDataDockCatalog(ctx context.Context, dockID, name string) error {
	dock, err := parseUUID("cluster_id", dockID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteDataDockCatalogWithResponse(ctx, dock, name)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete sql engine catalog", resp.StatusCode(), resp.Body)
}

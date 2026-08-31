// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// A service link carries no id and has no PATCH endpoint: it is addressed by the
// server-derived CRD name within its environment, and every field is immutable.

func (c *Client) CreateServiceLink(ctx context.Context, orgID, harborID string, body console.CreateServiceLinkRequestBody) (*console.ServiceLinkResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateServiceLinkWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create service link", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create service link: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) ListServiceLinks(ctx context.Context, orgID, harborID string) ([]console.ServiceLinkResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListServiceLinksWithResponse(ctx, org, harbor)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list service links", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: list service links: empty response")
	}
	return *resp.JSON200, nil
}

// FindServiceLink resolves a link by its CRD name, or ErrNotFound. There is no
// GET-by-name endpoint, so the harbor listing is the whole read path.
func (c *Client) FindServiceLink(ctx context.Context, orgID, harborID, name string) (*console.ServiceLinkResponse, error) {
	links, err := c.ListServiceLinks(ctx, orgID, harborID)
	if err != nil {
		return nil, err
	}
	return findByName(links, name, func(l *console.ServiceLinkResponse) string { return l.Name })
}

// DeleteServiceLink treats a 404 as success — the link is gone, which is what
// the caller asked for.
func (c *Client) DeleteServiceLink(ctx context.Context, orgID, harborID, name string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteServiceLinkWithResponse(ctx, org, harbor, name)
	if err != nil {
		return err
	}
	if err := statusErr("delete service link", resp.StatusCode(), resp.Body); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// ContainerAppDeclaredPorts returns the (port, protocol) pairs an app publishes,
// so a link's chosen target ports can be checked before the API rejects them
// with a 400 whose body carries no usable message.
//
// The app is resolved by slug, not by display name like FindContainerAppID: a
// ServiceRef.name travels to the API verbatim and is resolved there as a slug,
// so matching a display name would validate one app's ports while the API links
// another.
func (c *Client) ContainerAppDeclaredPorts(ctx context.Context, orgID, harborID, slug string) ([]console.ServiceLinkPort, error) {
	id, err := c.FindContainerAppIDBySlug(ctx, orgID, harborID, slug)
	if err != nil {
		return nil, err
	}
	spec, err := c.GetContainerAppSpec(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	out := make([]console.ServiceLinkPort, 0, len(spec.Ports))
	for _, p := range spec.Ports {
		out = append(out, console.ServiceLinkPort{Port: p.ContainerPort, Protocol: string(p.Protocol)})
	}
	return out, nil
}

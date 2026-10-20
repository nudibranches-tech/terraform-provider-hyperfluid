// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func (c *Client) CreateRabbitMQ(ctx context.Context, orgID, harborID string, body console.CreateHfRabbitMqCrdRequestBody) (*console.HfRabbitMqResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateHfRabbitmqCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create rabbitmq broker", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create rabbitmq broker: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetRabbitMQ(ctx context.Context, orgID, brokerID string) (*console.HfRabbitMqResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	broker, err := parseUUID("id", brokerID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetHfRabbitmqWithResponse(ctx, org, broker)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get rabbitmq broker", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get rabbitmq broker: empty response")
	}
	return resp.JSON200, nil
}

// GetRabbitMQSpec reads the broker's CRD spec view. It is the only shape that
// carries `resources`; its `image` is the operator's resolved image, so it is
// never used to populate the user's pinned `image`.
func (c *Client) GetRabbitMQSpec(ctx context.Context, orgID, brokerID string) (*console.HfRabbitMqCrdSpecResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	broker, err := parseUUID("id", brokerID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetHfRabbitmqCrdWithResponse(ctx, org, broker)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get rabbitmq broker spec", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get rabbitmq broker spec: empty response")
	}
	return resp.JSON200, nil
}

// FindRabbitMQ resolves a broker's id by name within an environment, or
// ErrNotFound. Backs the data source.
func (c *Client) FindRabbitMQ(ctx context.Context, orgID, harborID, name string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.ListHfRabbitmqsWithResponse(ctx, org, harbor)
	if err != nil {
		return "", err
	}
	if err := statusErr("list rabbitmq brokers", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", ErrNotFound
	}
	m, err := findByName(*resp.JSON200, name, func(x *console.HfRabbitMqResponse) string {
		return x.Name
	})
	if err != nil {
		return "", err
	}
	return m.Id.String(), nil
}

// FindEnvSlug resolves an environment's slug from its id, or ErrNotFound. The
// broker's secret-manager paths are keyed by the environment slug, which the
// broker response does not carry.
func (c *Client) FindEnvSlug(ctx context.Context, orgID, harborID string) (string, error) {
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return "", err
	}
	envs, err := c.ListHarbors(ctx, orgID)
	if err != nil {
		return "", err
	}
	h, err := find(envs, func(h *console.Harbor) bool { return h.Id == harbor })
	if err != nil {
		return "", err
	}
	return h.Slug, nil
}

func (c *Client) PatchRabbitMQ(ctx context.Context, orgID, brokerID string, body console.PatchHfRabbitMqCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	broker, err := parseUUID("id", brokerID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchHfRabbitmqCrdWithResponse(ctx, org, broker, body)
	if err != nil {
		return err
	}
	return statusErr("patch rabbitmq broker", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteRabbitMQ(ctx context.Context, orgID, brokerID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	broker, err := parseUUID("id", brokerID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteHfRabbitmqCrdWithResponse(ctx, org, broker)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete rabbitmq broker", resp.StatusCode(), resp.Body)
}

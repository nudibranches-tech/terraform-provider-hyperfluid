// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Airflow managed connection (sub-resource of an environment) ────────────
//
// Two identifiers matter here and they are not the same string. `conn_id` is
// what a DAG asks Airflow for; `name` is the connection CR's name, and it is
// the only one the API accepts in a URL. Every by-conn_id call therefore goes
// through FindAirflowConnection first, exactly as the data sources resolve a
// name into an id.

func (c *Client) CreateAirflowConnection(ctx context.Context, orgID, airflowID string, body console.CreateAirflowConnectionRequest) (*console.AirflowConnectionResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	inst, err := parseUUID("airflow", airflowID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateAirflowConnectionWithResponse(ctx, org, inst, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create airflow connection", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create airflow connection: empty response")
	}
	return resp.JSON201, nil
}

// GetAirflowConnection reads one connection by its CR name — the identifier the
// API addresses it by, which FindAirflowConnection resolves from a conn_id.
func (c *Client) GetAirflowConnection(ctx context.Context, orgID, airflowID, connectionName string) (*console.AirflowConnectionResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	inst, err := parseUUID("airflow", airflowID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetAirflowConnectionWithResponse(ctx, org, inst, connectionName)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get airflow connection", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get airflow connection %q: empty response", connectionName)
	}
	return resp.JSON200, nil
}

func (c *Client) ListAirflowConnections(ctx context.Context, orgID, airflowID string) ([]console.AirflowConnectionResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	inst, err := parseUUID("airflow", airflowID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.ListAirflowConnectionsWithResponse(ctx, org, inst)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list airflow connections", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: list airflow connections: empty response")
	}
	return *resp.JSON200, nil
}

// FindAirflowConnection resolves a connection by its user-facing conn_id, or
// ErrNotFound. The returned object carries the CR `name` every other call on
// this type needs.
func (c *Client) FindAirflowConnection(ctx context.Context, orgID, airflowID, connID string) (*console.AirflowConnectionResponse, error) {
	connections, err := c.ListAirflowConnections(ctx, orgID, airflowID)
	if err != nil {
		return nil, err
	}
	return findByName(connections, connID, func(x *console.AirflowConnectionResponse) string {
		return x.ConnId
	})
}

func (c *Client) PatchAirflowConnection(ctx context.Context, orgID, airflowID, connectionName string, body console.PatchAirflowConnectionRequest) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	inst, err := parseUUID("airflow", airflowID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchAirflowConnectionWithResponse(ctx, org, inst, connectionName, body)
	if err != nil {
		return err
	}
	return statusErr("patch airflow connection", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteAirflowConnection(ctx context.Context, orgID, airflowID, connectionName string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	inst, err := parseUUID("airflow", airflowID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteAirflowConnectionWithResponse(ctx, org, inst, connectionName)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete airflow connection", resp.StatusCode(), resp.Body)
}

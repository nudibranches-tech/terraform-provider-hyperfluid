// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── Airflow environment ────────────────────────────────────────────────────

func (c *Client) CreateAirflow(ctx context.Context, orgID, harborID string, body console.CreateAirflowCrdRequestBody) (*console.AirflowResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateAirflowCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create airflow", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create airflow: empty response")
	}
	return resp.JSON201, nil
}

// GetAirflow reads the DB projection: identity, tags, the resolved metadata
// cluster and the cached status. The fields the console does not cache
// (sleep_mode, egress, config, task quota, the live CR status) are on
// GetAirflowCrd, so a full read needs both.
func (c *Client) GetAirflow(ctx context.Context, orgID, instanceID string) (*console.AirflowResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetAirflowWithResponse(ctx, org, inst)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get airflow", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get airflow: empty response")
	}
	return resp.JSON200, nil
}

// GetAirflowCrd reads the live CRD spec plus the operator-owned status. It is
// the only view carrying sleep_mode, the egress grants, the airflow.cfg
// overrides, the task-pod quota and the task namespace / network mode /
// unresolved allow-lists — none of which the DB projection echoes.
func (c *Client) GetAirflowCrd(ctx context.Context, orgID, instanceID string) (*console.AirflowCrdSpecResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetAirflowCrdWithResponse(ctx, org, inst)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get airflow crd", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get airflow crd: empty response")
	}
	return resp.JSON200, nil
}

// FindAirflow resolves an environment's id by name within an environment's
// harbor, or ErrNotFound. Backs the data source.
func (c *Client) FindAirflow(ctx context.Context, orgID, harborID, name string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.ListAirflowsWithResponse(ctx, org, harbor)
	if err != nil {
		return "", err
	}
	if err := statusErr("list airflows", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", ErrNotFound
	}
	m, err := findByName(*resp.JSON200, name, func(x *console.AirflowResponse) string {
		return x.Name
	})
	if err != nil {
		return "", err
	}
	return m.Id.String(), nil
}

// PatchAirflow applies a partial update. The body's replace-not-merge fields
// (egress, config) and its clearing sentinels are the caller's concern; see
// airflowResource.Update.
func (c *Client) PatchAirflow(ctx context.Context, orgID, instanceID string, body console.PatchAirflowCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchAirflowCrdWithResponse(ctx, org, inst, body)
	if err != nil {
		return err
	}
	return statusErr("patch airflow", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteAirflow(ctx context.Context, orgID, instanceID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	inst, err := parseUUID("id", instanceID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteAirflowCrdWithResponse(ctx, org, inst)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete airflow", resp.StatusCode(), resp.Body)
}

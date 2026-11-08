// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// CreateConnectorIngestPipeline creates an open-data connector ingest pipeline.
// The result is an ordinary pipeline: it is read, updated and deleted through
// the generic pipeline calls.
func (c *Client) CreateConnectorIngestPipeline(ctx context.Context, body console.CreateConnectorIngestRequest) (*console.PipelineResponseData, error) {
	resp, err := c.api.CreateOpendataIngestionWithResponse(ctx, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create open data pipeline", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create open data pipeline: empty response")
	}
	return resp.JSON201, nil
}

// ListOpenDataConnectors returns the connector catalogue: each connector's
// slug and the JSON Schema of its params.
func (c *Client) ListOpenDataConnectors(ctx context.Context) ([]console.ConnectorDescriptorResponse, error) {
	resp, err := c.api.ListOpendataConnectorsWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if err := statusErr("list open data connectors", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: list open data connectors: empty response")
	}
	return resp.JSON200.Connectors, nil
}

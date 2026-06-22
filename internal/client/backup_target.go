// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ExternalBackupTargetInput is the flat, external-S3 create input. The create
// API takes a discriminated `source` union; this wrapper builds the `external`
// variant. (The `internal` variant — org Ceph bucket — is a follow-up.)
type ExternalBackupTargetInput struct {
	Name                      string
	EndpointURL               string
	DestinationPath           string
	AccessKeySecretName       string
	SecretAccessKeySecretName string
	Insecure                  *bool
	Description               *string
	Tags                      []string
}

func (c *Client) CreateExternalBackupTarget(ctx context.Context, orgID, harborID string, in ExternalBackupTargetInput) (*console.BackupTargetResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return nil, err
	}

	var source console.BackupTargetSourceRequest
	if err := source.FromBackupTargetSourceRequest1(console.BackupTargetSourceRequest1{
		Mode:                      "external",
		EndpointUrl:               in.EndpointURL,
		DestinationPath:           in.DestinationPath,
		AccessKeySecretName:       in.AccessKeySecretName,
		SecretAccessKeySecretName: in.SecretAccessKeySecretName,
	}); err != nil {
		return nil, fmt.Errorf("build backup target source: %w", err)
	}

	body := console.CreateBackupTargetCrdRequestBody{
		Name:        in.Name,
		Source:      source,
		Insecure:    in.Insecure,
		Description: in.Description,
	}
	if in.Tags != nil {
		body.Tags = &in.Tags
	}

	resp, err := c.api.CreateBackupTargetCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create backup target", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create backup target: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetBackupTarget(ctx context.Context, orgID, id string) (*console.BackupTargetResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	bt, err := parseUUID("id", id)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetBackupTargetWithResponse(ctx, org, bt)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get backup target", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get backup target: empty response")
	}
	return resp.JSON200, nil
}

// FindBackupTarget resolves a target's id by name within an environment, or
// ErrNotFound. Backs the data source.
func (c *Client) FindBackupTarget(ctx context.Context, orgID, harborID, name string) (string, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return "", err
	}
	harbor, err := parseUUID("harbor", harborID)
	if err != nil {
		return "", err
	}
	resp, err := c.api.ListBackupTargetsWithResponse(ctx, org, harbor)
	if err != nil {
		return "", err
	}
	if err := statusErr("list backup targets", resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", ErrNotFound
	}
	m, err := findByName(*resp.JSON200, name, func(x *console.BackupTargetResponse) string {
		return x.Name
	})
	if err != nil {
		return "", err
	}
	return m.Id.String(), nil
}

func (c *Client) PatchBackupTarget(ctx context.Context, orgID, id string, body console.PatchBackupTargetCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	bt, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchBackupTargetCrdWithResponse(ctx, org, bt, body)
	if err != nil {
		return err
	}
	return statusErr("patch backup target", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteBackupTarget(ctx context.Context, orgID, id string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	bt, err := parseUUID("id", id)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteBackupTargetCrdWithResponse(ctx, org, bt)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete backup target", resp.StatusCode(), resp.Body)
}

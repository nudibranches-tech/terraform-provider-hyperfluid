// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ErrGuestNotReady is a refused guest user: the virtual machine is not running,
// or its guest agent has not connected yet. Both clear on their own after a boot.
var ErrGuestNotReady = errors.New("hyperfluid: the virtual machine cannot take guest users yet")

// ── Virtual machine ────────────────────────────────────────────────────────

func (c *Client) CreateVirtualMachine(ctx context.Context, orgID, harborID string, body console.CreateVirtualMachineCrdRequestBody) (*console.VirtualMachineResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateVirtualMachineCrdWithResponse(ctx, org, harbor, body)
	if err != nil {
		return nil, err
	}
	if err := statusErr("create virtual machine", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create virtual machine: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetVirtualMachine(ctx context.Context, orgID, vmID string) (*console.VirtualMachineResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", vmID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetVirtualMachineWithResponse(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get virtual machine", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get virtual machine: empty response")
	}
	return resp.JSON200, nil
}

// FindVirtualMachine resolves a virtual machine by name within an environment,
// or ErrNotFound.
func (c *Client) FindVirtualMachine(ctx context.Context, orgID, harborID, name string) (*console.VirtualMachineResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	// The search is a substring match, so the exact name is picked from the page.
	limit := int64(200)
	resp, err := c.api.ListVirtualMachinesWithResponse(ctx, org, harbor, &console.ListVirtualMachinesParams{Search: &name, Limit: &limit})
	if err != nil {
		return nil, err
	}
	if err := statusErr("list virtual machines", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, ErrNotFound
	}
	return findByName(resp.JSON200.Data, name, func(vm *console.VirtualMachineResponse) string { return vm.Name })
}

func (c *Client) PatchVirtualMachine(ctx context.Context, orgID, vmID string, body console.PatchVirtualMachineCrdRequestBody) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	id, err := parseUUID("id", vmID)
	if err != nil {
		return err
	}
	resp, err := c.api.PatchVirtualMachineCrdWithResponse(ctx, org, id, body)
	if err != nil {
		return err
	}
	return statusErr("patch virtual machine", resp.StatusCode(), resp.Body)
}

func (c *Client) DeleteVirtualMachine(ctx context.Context, orgID, vmID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	id, err := parseUUID("id", vmID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteVirtualMachineCrdWithResponse(ctx, org, id)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete virtual machine", resp.StatusCode(), resp.Body)
}

// GetVirtualMachineImageCatalog lists the images an environment can boot.
func (c *Client) GetVirtualMachineImageCatalog(ctx context.Context, orgID, harborID string) (*console.VirtualMachineImageCatalog, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	harbor, err := parseUUID("environment", harborID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetVirtualMachineImageCatalogWithResponse(ctx, org, harbor)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get virtual machine image catalog", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get virtual machine image catalog: empty response")
	}
	return resp.JSON200, nil
}

// ── Virtual machine guest user ─────────────────────────────────────────────

// CreateVirtualMachineUser wraps a refusal for a guest that is not up yet in
// ErrGuestNotReady, so the caller can wait for it.
func (c *Client) CreateVirtualMachineUser(ctx context.Context, orgID, vmID string, body console.CreateVirtualMachineUserCrdRequestBody) (*console.VirtualMachineUserResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	vm, err := parseUUID("virtual_machine", vmID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.CreateVirtualMachineUserCrdWithResponse(ctx, org, vm, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() == http.StatusConflict && bytes.Contains(resp.Body, []byte("guest users")) {
		return nil, fmt.Errorf("%w: %s", ErrGuestNotReady, bytes.TrimSpace(resp.Body))
	}
	if err := statusErr("create virtual machine user", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("hyperfluid: create virtual machine user: empty response")
	}
	return resp.JSON201, nil
}

func (c *Client) GetVirtualMachineUser(ctx context.Context, orgID, vmID, userID string) (*console.VirtualMachineUserResponse, error) {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return nil, err
	}
	vm, err := parseUUID("virtual_machine", vmID)
	if err != nil {
		return nil, err
	}
	user, err := parseUUID("id", userID)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.GetVirtualMachineUserWithResponse(ctx, org, vm, user)
	if err != nil {
		return nil, err
	}
	if err := statusErr("get virtual machine user", resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("hyperfluid: get virtual machine user: empty response")
	}
	return resp.JSON200, nil
}

func (c *Client) DeleteVirtualMachineUser(ctx context.Context, orgID, vmID, userID string) error {
	org, err := parseUUID("organization_id", orgID)
	if err != nil {
		return err
	}
	vm, err := parseUUID("virtual_machine", vmID)
	if err != nil {
		return err
	}
	user, err := parseUUID("id", userID)
	if err != nil {
		return err
	}
	resp, err := c.api.DeleteVirtualMachineUserCrdWithResponse(ctx, org, vm, user)
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return statusErr("delete virtual machine user", resp.StatusCode(), resp.Body)
}

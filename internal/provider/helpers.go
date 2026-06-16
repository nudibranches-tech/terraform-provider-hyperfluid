// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// firstNonEmpty returns the first non-empty string (config → env fallback).
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// splitHarborName parses a "harbor/name" import id into its two parts.
func splitHarborName(id string) (harbor, name string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected import id in the form \"harbor/name\", got %q", id)
	}
	return parts[0], parts[1], nil
}

// waitForReady polls until poll reports ready, the context is cancelled, or the
// timeout elapses. poll returns (value, ready, error); a transient error from
// poll aborts the wait.
func waitForReady[T any](ctx context.Context, timeout time.Duration, poll func() (T, bool, error)) (T, error) {
	var zero T
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		v, ready, err := poll()
		if err != nil {
			return zero, err
		}
		if ready {
			return v, nil
		}
		if time.Now().After(deadline) {
			return zero, fmt.Errorf("timed out after %s waiting for resource to become ready", timeout)
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-ticker.C:
		}
	}
}

// pollGoneOn404 polls get until it returns client.ErrNotFound, confirming a
// delete actually converged (M3: the API may 204 before the resource is gone).
func pollGoneOn404(ctx context.Context, timeout time.Duration, get func() error) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		err := get()
		if errors.Is(err, client.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for resource deletion to converge", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

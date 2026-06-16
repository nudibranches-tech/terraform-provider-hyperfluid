// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// ── tfsdk <-> API value conversions ────────────────────────────────────────

// stringPtr returns nil for null/unknown, else a pointer to the value.
func stringPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func boolPtr(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}

// enabledOrDefault defaults a null/unknown bool to true (for required create fields).
func enabledOrDefault(v types.Bool) bool {
	if v.IsNull() || v.IsUnknown() {
		return true
	}
	return v.ValueBool()
}

func int32PtrFromInt64(v types.Int64) *int32 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	n := int32(v.ValueInt64())
	return &n
}

// optString maps an optional API string to a tfsdk value (nil → null).
func optString(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

func optInt64FromInt32(n *int32) types.Int64 {
	if n == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*n))
}

// stringSliceToList converts an API []string into a tfsdk list value.
func stringSliceToList(ctx context.Context, s []string) (types.List, diag.Diagnostics) {
	return types.ListValueFrom(ctx, types.StringType, s)
}

// listToStringSlice converts a tfsdk list into []string (nil for null/unknown).
func listToStringSlice(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	d := l.ElementsAs(ctx, &out, false)
	return out, d
}

// parseStorageGB reads the leading integer of a K8s quantity like "10Gi" → 10.
func parseStorageGB(s string) int64 {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

// splitID splits a composite "<a>/<b>" import id into its two parts.
func splitID(id string) (first, second string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected import id in the form \"<parent>/<child>\", got %q", id)
	}
	return parts[0], parts[1], nil
}

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

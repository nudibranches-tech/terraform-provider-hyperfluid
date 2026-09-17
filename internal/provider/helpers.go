// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// timeString formats an API timestamp as RFC3339 for a computed string attribute.
func timeString(t time.Time) types.String {
	return types.StringValue(t.Format(time.RFC3339))
}

// optTimeString formats an optional API timestamp (nil → null).
func optTimeString(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.Format(time.RFC3339))
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

// stringSliceToSet converts an API []string into a tfsdk set value (order-insensitive).
func stringSliceToSet(ctx context.Context, s []string) (types.Set, diag.Diagnostics) {
	return types.SetValueFrom(ctx, types.StringType, s)
}

// setToStringSlice converts a tfsdk set into []string (nil for null/unknown).
func setToStringSlice(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	var out []string
	d := s.ElementsAs(ctx, &out, false)
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

// splitEnvName parses a "env/name" import id into its two parts.
func splitEnvName(id string) (env, name string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected import id in the form \"env/name\", got %q", id)
	}
	return parts[0], parts[1], nil
}

// pollInterval is how long every wait below sleeps between two attempts.
const pollInterval = 3 * time.Second

// pollRetryBudget is how many CONSECUTIVE retryable failures a wait absorbs
// before it gives up. The budget exists because these waits run for a long time — an
// Airflow environment provisions a metadata PostgreSQL, a DAG bucket, a
// database migration and four components inside a 15-minute ceiling — and over
// such a window one 502 from the ingress, or one 500 from an endpoint reading a
// half-provisioned object, is an ordinary event and not a verdict. Ending the
// apply on the first of them fails a create that was going to succeed, for a
// reason the user cannot act on.
//
// It is a budget rather than "retry forever" so a genuinely broken call still
// surfaces: the fifth consecutive failure ends the wait carrying the last
// error, which costs at most four extra attempts. Failures are counted
// consecutively and reset by any successful attempt, so an endpoint that flaps
// once a minute for fifteen minutes still converges instead of exhausting a
// global allowance.
const pollRetryBudget = 4

// pollErrGone reports the two errors that are an answer about the resource
// rather than a failure to ask: it is gone, or the console can no longer resolve
// its authz scope because it is gone. Both are decisive — retrying either would
// turn a verdict into a timeout — and pollGoneOn404 reads the same two as the
// state it is waiting for.
func pollErrGone(err error) bool {
	return errors.Is(err, client.ErrNotFound) || errors.Is(err, client.ErrForbidden)
}

// pollErrRetryable reports whether a failed attempt is worth repeating: the ask
// failed, rather than the resource having answered something.
//
// Exactly two failures qualify. A transport error means the request never
// reached a handler at all. A 5xx means it reached one that could not answer —
// an ingress 502 while a route is being rewritten, or a console endpoint that
// read a half-provisioned object. Both are routine inside a 15-minute wait and
// neither says anything about the resource.
//
// Everything else stands. A 4xx is a request that will be refused identically
// however often it is repeated. And a plain error from the poll closure is its
// own VERDICT — a `Failed` backup target, a colliding Airflow connection, an
// environment in `Error` — which those polls raise precisely so the apply ends
// at once instead of burning the whole ceiling; retrying it would undo the
// fail-fast and bury the reason under a retry count.
func pollErrRetryable(err error) bool {
	if err == nil || pollErrGone(err) {
		return false
	}
	var serverErr *client.ServerError
	if errors.As(err, &serverErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// waitForReady polls until poll reports ready, the context is cancelled, or the
// timeout elapses. poll returns (value, ready, error); an attempt that failed to
// reach an answer is retried within pollRetryBudget, and every other error ends
// the wait at once (pollErrRetryable).
func waitForReady[T any](ctx context.Context, timeout time.Duration, poll func() (T, bool, error)) (T, error) {
	return waitForReadyEvery(ctx, timeout, pollInterval, poll)
}

// waitForReadyEvery is waitForReady with the interval injected, so a test can
// exercise the retry budget without sleeping through it.
func waitForReadyEvery[T any](ctx context.Context, timeout, interval time.Duration, poll func() (T, bool, error)) (T, error) {
	var zero T
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	var lastErr error
	for {
		v, ready, err := poll()
		switch {
		case err == nil:
			if ready {
				return v, nil
			}
			failures, lastErr = 0, nil
		case !pollErrRetryable(err):
			return zero, err
		default:
			failures, lastErr = failures+1, err
			if failures > pollRetryBudget {
				return zero, fmt.Errorf("gave up after %d consecutive failed attempts: %w", failures, err)
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return zero, fmt.Errorf("timed out after %s waiting for resource to become ready; the last attempt failed: %w", timeout, lastErr)
			}
			return zero, fmt.Errorf("timed out after %s waiting for resource to become ready", timeout)
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-ticker.C:
		}
	}
}

// conditionMessage scans Kubernetes-style status conditions — which the
// generated client models as an untyped JSON value (`interface{}`) — and returns
// the message of the named condition, or "" if it is absent or has no message.
func conditionMessage(conditions interface{}, condType string) string {
	arr, ok := conditions.([]interface{})
	if !ok {
		return ""
	}
	for _, c := range arr {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == condType {
			if msg, _ := m["message"].(string); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// pollGoneOn404 polls get until it returns client.ErrNotFound, confirming a
// delete actually converged (M3: the API may 204 before the resource is gone).
func pollGoneOn404(ctx context.Context, timeout time.Duration, get func() error) error {
	return pollGoneOn404Every(ctx, timeout, pollInterval, get)
}

// pollGoneOn404Every is pollGoneOn404 with the interval injected, for tests.
func pollGoneOn404Every(ctx context.Context, timeout, interval time.Duration, get func() error) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	var lastErr error
	for {
		err := get()
		switch {
		// ErrForbidden counts as gone alongside ErrNotFound: the console resolves
		// a resource's authz scope by looking the resource up, so a deleted
		// resource is denied rather than reported missing. The DELETE itself
		// already succeeded here.
		case pollErrGone(err):
			return nil
		case err == nil:
			failures, lastErr = 0, nil
		// A 502 from the ingress while the object's finalizer runs is not a
		// resource that failed to go away; reporting it as one hands the user a
		// cleanup they do not have to do.
		case pollErrRetryable(err):
			failures, lastErr = failures+1, err
			if failures > pollRetryBudget {
				return fmt.Errorf("gave up after %d consecutive failed attempts: %w", failures, err)
			}
		default:
			return err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timed out after %s waiting for resource deletion to converge; the last attempt failed: %w", timeout, lastErr)
			}
			return fmt.Errorf("timed out after %s waiting for resource deletion to converge", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

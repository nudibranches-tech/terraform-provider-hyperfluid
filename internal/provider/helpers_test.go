// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

func TestConditionMessage(t *testing.T) {
	// Conditions as the generated client models them: an untyped JSON array of
	// objects (decoded to []interface{} of map[string]interface{}).
	conditions := []interface{}{
		map[string]interface{}{"type": "SecretApplied", "status": "True", "message": "ok"},
		map[string]interface{}{
			"type":    "S3Reachable",
			"status":  "False",
			"reason":  "BucketUnreachable",
			"message": "HeadBucket on 'my-backups' failed: access denied",
		},
	}

	tests := []struct {
		name       string
		conditions interface{}
		condType   string
		want       string
	}{
		{"found", conditions, "S3Reachable", "HeadBucket on 'my-backups' failed: access denied"},
		{"other condition", conditions, "SecretApplied", "ok"},
		{"missing type", conditions, "ObjectStoreApplied", ""},
		{"nil", nil, "S3Reachable", ""},
		{"wrong shape", "not-an-array", "S3Reachable", ""},
		{"empty array", []interface{}{}, "S3Reachable", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conditionMessage(tt.conditions, tt.condType); got != tt.want {
				t.Errorf("conditionMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── poll retry tolerance ──────────────────────────────────────────────────

// testPollInterval keeps the retry tests off the real 3-second cadence; the
// budget is what is under test, not the wait between attempts.
const testPollInterval = time.Millisecond

// badGateway is what the ingress hands back while a route is being rewritten,
// and transportDown what a request that never reached a handler looks like.
// Both are failures to ask, not answers about the resource.
var (
	badGateway    = &client.ServerError{Op: "get airflow crd", Status: 502, Body: "<html>502 Bad Gateway</html>"}
	transportDown = &url.Error{Op: "Get", URL: "https://console.example.com/api/v1", Err: errors.New("dial tcp: connection refused")}
	// A verdict the poll itself reached about what it read — the shape
	// backup_target and the two Airflow resources use to fail fast.
	verdict = errors.New("the environment reported phase Error: metadata migration failed")
	// A request the server refused, and will refuse identically next time.
	badRequest = errors.New("hyperfluid: create airflow -> 400: name is already taken")
)

func TestWaitForReadyRetriesTransientFailures(t *testing.T) {
	ctx := t.Context()

	t.Run("a transient failure is retried, not fatal", func(t *testing.T) {
		calls := 0
		got, err := waitForReadyEvery(ctx, time.Minute, testPollInterval, func() (string, bool, error) {
			calls++
			if calls == 1 {
				return "", false, badGateway
			}
			return "settled", true, nil
		})
		if err != nil {
			t.Fatalf("a single 502 must not fail the wait: %v", err)
		}
		if got != "settled" {
			t.Errorf("value = %q, want the value the successful attempt returned", got)
		}
		if calls != 2 {
			t.Errorf("attempts = %d, want 2", calls)
		}
	})

	t.Run("the whole budget is usable", func(t *testing.T) {
		calls := 0
		_, err := waitForReadyEvery(ctx, time.Minute, testPollInterval, func() (string, bool, error) {
			calls++
			if calls <= pollRetryBudget {
				return "", false, badGateway
			}
			return "settled", true, nil
		})
		if err != nil {
			t.Fatalf("%d consecutive failures are inside the budget: %v", pollRetryBudget, err)
		}
	})

	t.Run("the budget is bounded", func(t *testing.T) {
		calls := 0
		_, err := waitForReadyEvery(ctx, time.Minute, testPollInterval, func() (string, bool, error) {
			calls++
			return "", false, badGateway
		})
		if err == nil {
			t.Fatal("a call that never succeeds must end the wait")
		}
		if !errors.Is(err, badGateway) {
			t.Errorf("error = %v, want it to carry the underlying failure", err)
		}
		if calls != pollRetryBudget+1 {
			t.Errorf("attempts = %d, want %d (the budget plus the attempt that opened it)", calls, pollRetryBudget+1)
		}
	})

	t.Run("a success resets the budget", func(t *testing.T) {
		// Far more failures in total than the budget, never two in a row: a
		// flapping endpoint must not exhaust a global allowance.
		calls := 0
		_, err := waitForReadyEvery(ctx, time.Minute, testPollInterval, func() (string, bool, error) {
			calls++
			switch {
			case calls > 4*pollRetryBudget:
				return "settled", true, nil
			case calls%2 == 1:
				return "", false, badGateway
			default:
				return "", false, nil
			}
		})
		if err != nil {
			t.Fatalf("alternating failures must not exhaust the budget: %v", err)
		}
	})

	t.Run("a transport failure is retried too", func(t *testing.T) {
		calls := 0
		_, err := waitForReadyEvery(ctx, time.Minute, testPollInterval, func() (string, bool, error) {
			calls++
			if calls == 1 {
				return "", false, transportDown
			}
			return "settled", true, nil
		})
		if err != nil {
			t.Fatalf("a refused connection must not fail the wait: %v", err)
		}
	})

	t.Run("not found stays terminal", func(t *testing.T) {
		calls := 0
		_, err := waitForReadyEvery(ctx, time.Minute, testPollInterval, func() (string, bool, error) {
			calls++
			return "", false, fmt.Errorf("reading it back: %w", client.ErrNotFound)
		})
		if !errors.Is(err, client.ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound", err)
		}
		if calls != 1 {
			t.Errorf("attempts = %d, want 1: a 404 is an answer, not a failure to ask", calls)
		}
	})

	t.Run("forbidden stays terminal", func(t *testing.T) {
		calls := 0
		_, err := waitForReadyEvery(ctx, time.Minute, testPollInterval, func() (string, bool, error) {
			calls++
			return "", false, fmt.Errorf("reading it back: %w", client.ErrForbidden)
		})
		if !errors.Is(err, client.ErrForbidden) {
			t.Fatalf("error = %v, want ErrForbidden", err)
		}
		if calls != 1 {
			t.Errorf("attempts = %d, want 1", calls)
		}
	})

	t.Run("a timeout reached while retrying reports the failure", func(t *testing.T) {
		_, err := waitForReadyEvery(ctx, 0, testPollInterval, func() (string, bool, error) {
			return "", false, badGateway
		})
		if !errors.Is(err, badGateway) {
			t.Fatalf("error = %v, want the last failure to survive the timeout", err)
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error = %q, want it to say the wait timed out", err)
		}
	})

	t.Run("a plain timeout is unchanged", func(t *testing.T) {
		_, err := waitForReadyEvery(ctx, 0, testPollInterval, func() (string, bool, error) {
			return "", false, nil
		})
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("error = %v, want a timeout", err)
		}
		if strings.Contains(err.Error(), "the last attempt failed") {
			t.Errorf("error = %q, want no failure mentioned when there was none", err)
		}
	})
}

func TestPollGoneOn404RetriesTransientFailures(t *testing.T) {
	ctx := t.Context()

	t.Run("a transient failure is retried", func(t *testing.T) {
		calls := 0
		err := pollGoneOn404Every(ctx, time.Minute, testPollInterval, func() error {
			calls++
			if calls == 1 {
				return badGateway
			}
			return client.ErrNotFound
		})
		if err != nil {
			t.Fatalf("a 502 while the finalizer runs must not fail the delete: %v", err)
		}
		if calls != 2 {
			t.Errorf("attempts = %d, want 2", calls)
		}
	})

	t.Run("the budget is bounded", func(t *testing.T) {
		calls := 0
		err := pollGoneOn404Every(ctx, time.Minute, testPollInterval, func() error {
			calls++
			return badGateway
		})
		if !errors.Is(err, badGateway) {
			t.Fatalf("error = %v, want it to carry the underlying failure", err)
		}
		if calls != pollRetryBudget+1 {
			t.Errorf("attempts = %d, want %d", calls, pollRetryBudget+1)
		}
	})

	t.Run("gone on the first look", func(t *testing.T) {
		for name, gone := range map[string]error{"not found": client.ErrNotFound, "forbidden": client.ErrForbidden} {
			t.Run(name, func(t *testing.T) {
				calls := 0
				err := pollGoneOn404Every(ctx, time.Minute, testPollInterval, func() error {
					calls++
					return fmt.Errorf("reading it back: %w", gone)
				})
				if err != nil {
					t.Fatalf("err = %v, want the resource reported gone", err)
				}
				if calls != 1 {
					t.Errorf("attempts = %d, want 1", calls)
				}
			})
		}
	})

	t.Run("still present at the deadline", func(t *testing.T) {
		err := pollGoneOn404Every(ctx, 0, testPollInterval, func() error { return nil })
		if err == nil || !strings.Contains(err.Error(), "deletion to converge") {
			t.Fatalf("error = %v, want a deletion timeout", err)
		}
	})
}

// TestPollErrClassification pins which failures are worth repeating. The two
// that are say nothing about the resource; everything else is an answer, and
// repeating an answer only delays it.
func TestPollErrClassification(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		wantRetryable bool
		wantGone      bool
	}{
		{"a 5xx", badGateway, true, false},
		{"a 5xx, wrapped", fmt.Errorf("reading it back: %w", badGateway), true, false},
		{"a transport failure", transportDown, true, false},
		{"the poll's own verdict", verdict, false, false},
		{"a 4xx", badRequest, false, false},
		{"not found", client.ErrNotFound, false, true},
		{"forbidden", fmt.Errorf("get x: %w", client.ErrForbidden), false, true},
		{"no error at all", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pollErrRetryable(tc.err); got != tc.wantRetryable {
				t.Errorf("pollErrRetryable = %v, want %v", got, tc.wantRetryable)
			}
			if got := pollErrGone(tc.err); got != tc.wantGone {
				t.Errorf("pollErrGone = %v, want %v", got, tc.wantGone)
			}
		})
	}
}

// TestWaitForReadyKeepsFailingFast is the other half of the tolerance: the
// polls that raise a verdict of their own — a Failed backup target, a colliding
// Airflow connection, an environment in Error — do so to end the apply at once
// with the reason in hand. Retrying those would undo the fail-fast and bury the
// reason under a retry count.
func TestWaitForReadyKeepsFailingFast(t *testing.T) {
	ctx := t.Context()
	for name, err := range map[string]error{"a verdict": verdict, "a 4xx": badRequest} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			_, got := waitForReadyEvery(ctx, time.Minute, testPollInterval, func() (string, bool, error) {
				calls++
				return "", false, err
			})
			if !errors.Is(got, err) {
				t.Fatalf("error = %v, want %v verbatim", got, err)
			}
			if calls != 1 {
				t.Errorf("attempts = %d, want 1", calls)
			}
		})
	}
}

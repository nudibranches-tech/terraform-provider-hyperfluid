// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"errors"
	"net/http"
	"testing"
)

// TestStatusErrTypesServerErrors pins both halves of the contract the provider's
// poll retry depends on: a 5xx is matchable as a *ServerError, and nothing else
// is — least of all a 4xx, which repeating would only delay.
func TestStatusErrTypesServerErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   bool
	}{
		{"internal error", http.StatusInternalServerError, true},
		{"bad gateway", http.StatusBadGateway, true},
		{"service unavailable", http.StatusServiceUnavailable, true},
		{"gateway timeout", http.StatusGatewayTimeout, true},
		{"bad request", http.StatusBadRequest, false},
		{"conflict", http.StatusConflict, false},
		{"not found", http.StatusNotFound, false},
		{"forbidden", http.StatusForbidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := statusErr("get airflow crd", tc.status, []byte("boom"))
			var serverErr *ServerError
			if got := errors.As(err, &serverErr); got != tc.want {
				t.Fatalf("matched as *ServerError = %v, want %v (error: %v)", got, tc.want, err)
			}
			if tc.want && serverErr.Status != tc.status {
				t.Errorf("status = %d, want %d", serverErr.Status, tc.status)
			}
		})
	}

	// The sentinels those two statuses carry are what the delete polls read as
	// "gone"; typing the 5xx must not have disturbed them.
	if !errors.Is(statusErr("get x", http.StatusNotFound, nil), ErrNotFound) {
		t.Error("a 404 no longer reports ErrNotFound")
	}
	if !errors.Is(statusErr("get x", http.StatusForbidden, []byte(`{"message":"nope"}`)), ErrForbidden) {
		t.Error("a 403 no longer reports ErrForbidden")
	}
	if err := statusErr("get x", http.StatusOK, nil); err != nil {
		t.Errorf("a 2xx reported %v", err)
	}

	// The message is the one users have always seen: giving the error a type
	// was meant to make it matchable, not to reword it.
	const want = "hyperfluid: get airflow crd -> 502: <html>502 Bad Gateway</html>"
	if got := statusErr("get airflow crd", http.StatusBadGateway,
		[]byte(" <html>502 Bad Gateway</html>\n")).Error(); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

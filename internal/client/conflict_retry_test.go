// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// A write the console gave up on because the agent kept changing is sent
// again, body included, a bounded number of times; any other 409 is a verdict
// and is returned at once.
func TestConflictRetry(t *testing.T) {
	conflictBackoff = func(int) time.Duration { return time.Millisecond }
	org, agent := uuid.NewString(), uuid.NewString()
	cases := []struct {
		name      string
		conflicts int    // how many 409s the console answers first
		message   string // their message
		wantCalls int
		wantErr   bool
	}{
		{"resolves after two conflicts", 2, "the agent was changed by someone else while this change was being written; try again", 3, false},
		{"gives up after four attempts", 99, "the agent was changed by someone else while this change was being written; try again", 4, true},
		{"another 409 is not retried", 99, "the widget is off: an update never turns it on; enable it with PUT", 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil || body["privacy_url"] != "https://example.org/privacy" {
					t.Errorf("attempt %d lost the body: %s", calls, raw)
				}
				w.Header().Set("Content-Type", "application/json")
				if calls <= tc.conflicts {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": tc.message})
					return
				}
				_ = json.NewEncoder(w).Encode(console.AiAgentWidgetResponse{Enabled: true, Phase: "Active", Warnings: []string{}})
			}))
			defer srv.Close()
			c, err := New(srv.URL, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.UpdateAiAgentWidget(t.Context(), org, agent, console.AiAgentWidgetConfig{PrivacyUrl: "https://example.org/privacy"})
			if calls != tc.wantCalls || (err != nil) != tc.wantErr {
				t.Fatalf("calls = %d, err = %v", calls, err)
			}
			if tc.wantErr && IsConflict(err) != (tc.conflicts == 99 && tc.wantCalls == 4) {
				t.Errorf("IsConflict(%v) is wrong", err)
			}
		})
	}
}

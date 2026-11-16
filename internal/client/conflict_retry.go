// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"
)

// The console writes an agent's custom resource with a read-modify-write
// conditioned on its version, and gives up with a 409 when someone else keeps
// writing it in between ("the agent was changed by someone else while this
// change was being written; try again"). Nothing about the request was wrong,
// so the client sends it again, a bounded number of times, before handing the
// 409 to the caller.
//
// Only that conflict is retried. The other 409s of the agent routes are
// verdicts (a widget that is off, a Matrix re-login that is not possible) and
// asking again would only delay them.

const (
	conflictAttempts = 4 // the first try and three more
	conflictMarker   = "changed by someone else"
)

// conflictBackoff is how long the n-th retry waits; a test shortens it.
var conflictBackoff = func(n int) time.Duration { return time.Duration(n) * 250 * time.Millisecond }

type conflictRetry struct{ next http.RoundTripper }

func (t conflictRetry) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet || (req.Body != nil && req.GetBody == nil) {
		return t.next.RoundTrip(req)
	}
	for attempt := 1; ; attempt++ {
		r := req
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r = req.Clone(req.Context())
			r.Body = body
		}
		resp, err := t.next.RoundTrip(r)
		if err != nil || resp.StatusCode != http.StatusConflict || attempt == conflictAttempts {
			return resp, err
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		if readErr != nil || !strings.Contains(string(raw), conflictMarker) {
			return resp, readErr
		}
		select {
		case <-req.Context().Done():
			return resp, nil
		case <-time.After(conflictBackoff(attempt)):
		}
	}
}

// IsConflict reports a write the console gave up on because the agent kept
// changing under it, after the client's own retries.
func IsConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), conflictMarker)
}

// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// The console's OpenAPI declares every error response as an `ApiErrorBody`
// object, but several of its error paths — `NotFound` among them — serialise the
// message as a bare JSON string instead. The generated client trusts the spec:
// it fails to decode such a body and returns that decode error *in place of* the
// response, so the status code is lost with it. A 404 then reads as a transport
// failure, which breaks both drift detection (Read → remove from state) and the
// delete-confirmation polls.
//
// This transport rewrites a bare-string error body into the object the spec
// promises, so the status always survives. It is a no-op for a body that already
// matches, and so stays inert once the console is fixed.
type errorBodyShim struct{ next http.RoundTripper }

func (t errorBodyShim) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.StatusCode < 400 {
		return resp, err
	}

	raw, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return resp, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))

	var message string
	if json.Unmarshal(raw, &message) != nil {
		return resp, nil // not a bare string: leave it alone
	}
	rewritten, marshalErr := json.Marshal(map[string]string{"message": message})
	if marshalErr != nil {
		return resp, nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", "")
	return resp, nil
}

// withErrorBodyShim wraps an HTTP client's transport. The bearer-token transport
// stays underneath, so token injection and refresh are untouched.
func withErrorBodyShim(c *http.Client) *http.Client {
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	shimmed := *c
	shimmed.Transport = errorBodyShim{next: next}
	return &shimmed
}

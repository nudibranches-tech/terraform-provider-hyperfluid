// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

type stubTransport struct {
	status int
	body   string
}

func (s stubTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(bytes.NewReader([]byte(s.body))),
		Header:     http.Header{},
	}, nil
}

func roundTrip(t *testing.T, status int, body string) string {
	t.Helper()
	resp, err := errorBodyShim{next: stubTransport{status: status, body: body}}.RoundTrip(&http.Request{})
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(got)
}

func TestErrorBodyShim(t *testing.T) {
	// The console's actual 404: a bare JSON string where the spec promises an object.
	if got := roundTrip(t, 404, `"cache 'x' not found"`); got != `{"message":"cache 'x' not found"}` {
		t.Errorf("bare string not rewritten: %s", got)
	}
	// Already an object: untouched, so the shim goes inert once the console is fixed.
	obj := `{"message":"nope","resource":"cache"}`
	if got := roundTrip(t, 404, obj); got != obj {
		t.Errorf("object body was rewritten: %s", got)
	}
	// Success bodies are never touched, even when they are a bare string.
	if got := roundTrip(t, 200, `"plain"`); got != `"plain"` {
		t.Errorf("success body was rewritten: %s", got)
	}
	// A non-JSON error body is passed through rather than mangled.
	if got := roundTrip(t, 500, `upstream exploded`); got != `upstream exploded` {
		t.Errorf("non-JSON body was rewritten: %s", got)
	}
}

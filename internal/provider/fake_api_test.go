// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// fakeAPI is a console stand-in whose routes a test declares: each answers a
// method and a path pattern, and every request is recorded for assertions.
type fakeAPI struct {
	t   *testing.T
	org uuid.UUID
	env uuid.UUID

	mu       sync.Mutex
	routes   []fakeRoute
	requests []fakeCall
}

type fakeRoute struct {
	method string
	path   *regexp.Regexp
	handle func(path string, body []byte) (int, any)
}

type fakeCall struct {
	method, path string
	body         map[string]any
}

func newFakeAPI(t *testing.T) (*fakeAPI, *providerData) {
	t.Helper()
	f := &fakeAPI{t: t, org: uuid.New(), env: uuid.New()}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	api, err := client.New(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return f, &providerData{API: api, OrgID: f.org.String()}
}

// on routes method requests whose path matches pattern (anchored at the end)
// to handle. The first matching route answers.
func (f *fakeAPI) on(method, pattern string, handle func(path string, body []byte) (int, any)) {
	f.routes = append(f.routes, fakeRoute{method: method, path: regexp.MustCompile(pattern + `$`), handle: handle})
}

// calls returns the recorded requests of that method whose path matches pattern.
func (f *fakeAPI) calls(method, pattern string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	re := regexp.MustCompile(pattern + `$`)
	var out []fakeCall
	for _, c := range f.requests {
		if c.method == method && re.MatchString(c.path) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Errorf("%s %s: body is not JSON: %v", r.Method, r.URL.Path, err)
		}
	}
	f.requests = append(f.requests, fakeCall{method: r.Method, path: r.URL.Path, body: body})
	for _, route := range f.routes {
		if route.method == r.Method && route.path.MatchString(r.URL.Path) {
			code, v := route.handle(r.URL.Path, raw)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			if v != nil {
				_ = json.NewEncoder(w).Encode(v)
			}
			return
		}
	}
	f.t.Errorf("unexpected request %s %s (body %s)", r.Method, r.URL.Path, raw)
	w.WriteHeader(http.StatusNotFound)
}

// decodeInto unmarshals a request body, failing the test on a malformed one.
func decodeInto[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Errorf("request body: %v", err)
	}
	return v
}

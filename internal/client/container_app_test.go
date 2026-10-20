// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

type jsonDoer string

func (b jsonDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(b))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func TestFindContainerAppIDBySlugIgnoresDisplayNames(t *testing.T) {
	const (
		org    = "00000000-0000-0000-0000-000000000001"
		harbor = "00000000-0000-0000-0000-000000000002"
	)
	// The first app's display name is the second app's slug.
	list := `[
		{"id":"00000000-0000-0000-0000-00000000000a","name":"web","slug":"storefront","harbor_id":"` + harbor + `","organization_id":"` + org + `"},
		{"id":"00000000-0000-0000-0000-00000000000b","name":"Web","slug":"web","harbor_id":"` + harbor + `","organization_id":"` + org + `"}
	]`
	api, err := console.NewClientWithResponses("http://console.invalid", console.WithHTTPClient(jsonDoer(list)))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{api: api}

	got, err := c.FindContainerAppIDBySlug(context.Background(), org, harbor, "web")
	if err != nil {
		t.Fatal(err)
	}
	if want := "00000000-0000-0000-0000-00000000000b"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}

	if _, err := c.FindContainerAppIDBySlug(context.Background(), org, harbor, "Web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a display name resolved an app: err = %v", err)
	}
}

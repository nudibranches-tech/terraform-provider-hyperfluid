// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"errors"
	"strings"
	"testing"
)

func TestNeedsNamesThePermissionOfABareRefusal(t *testing.T) {
	bare := statusErr("create grant", 403, []byte(`{"message":"You are not authorized to manage authorization in this organization"}`))
	got := needs(bare, "grant:create")
	if !errors.Is(got, ErrForbidden) || !strings.Contains(got.Error(), `requires the "grant:create" permission`) {
		t.Errorf("bare refusal = %v", got)
	}

	// A refusal that already names its permission, or refuses for another
	// reason, is left as it is.
	for _, body := range []string{
		`{"message":"Missing permission 'grant:create'","required_permission":"grant:create"}`,
		`{"message":"System role permission bundles are managed by the platform and cannot be edited"}`,
	} {
		err := statusErr("set role permissions", 403, []byte(body))
		if got := needs(err, "role:update"); got.Error() != err.Error() {
			t.Errorf("needs changed %q into %q", err, got)
		}
	}
	if needs(nil, "grant:create") != nil {
		t.Error("success must stay nil")
	}
	if got := needs(ErrNotFound, "grant:read"); !errors.Is(got, ErrNotFound) || got != ErrNotFound {
		t.Errorf("a 404 must be left alone: %v", got)
	}
}

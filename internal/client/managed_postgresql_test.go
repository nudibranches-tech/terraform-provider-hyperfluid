// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"testing"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func TestPatchManagedPostgresqlBodyClearsInterval(t *testing.T) {
	desc := "orders"
	body := console.PatchManagedPostgresqlCrdRequestBody{Description: &desc, Pitr: &console.PitrRequest{Enabled: true}}

	raw, err := patchManagedPostgresqlBody(body, PatchManagedPostgresqlOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"description":"orders","pitr":{"enabled":true}}` {
		t.Errorf("without the option: %s", got)
	}

	raw, err = patchManagedPostgresqlBody(body, PatchManagedPostgresqlOptions{ClearArchiveInterval: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"description":"orders","pitr":{"archive_interval_seconds":null,"enabled":true}}` {
		t.Errorf("clearing: %s", got)
	}

	if _, err := patchManagedPostgresqlBody(console.PatchManagedPostgresqlCrdRequestBody{}, PatchManagedPostgresqlOptions{ClearArchiveInterval: true}); err == nil {
		t.Error("clearing without a pitr block was accepted")
	}
	pinned := int32(300)
	body.Pitr.ArchiveIntervalSeconds = &pinned
	if _, err := patchManagedPostgresqlBody(body, PatchManagedPostgresqlOptions{ClearArchiveInterval: true}); err == nil {
		t.Error("an interval both set and cleared was accepted")
	}
}

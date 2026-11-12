// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func pitrObject(t *testing.T, enabled bool, interval types.Int64) types.Object {
	t.Helper()
	o, d := types.ObjectValueFrom(context.Background(), pitrType().AttrTypes, pitrModel{
		Enabled: types.BoolValue(enabled), ArchiveIntervalSeconds: interval,
	})
	mustSucceed(t, d)
	return o
}

// patchedPitr returns the pitr object of the n-th PATCH, and whether the body
// carried one.
func patchedPitr(t *testing.T, f *fakeAPI, n int) (map[string]any, bool) {
	t.Helper()
	patches := f.calls("PATCH", `/crd`)
	if len(patches) <= n {
		t.Fatalf("%d PATCH, want at least %d", len(patches), n+1)
	}
	raw, ok := patches[n].body["pitr"]
	if !ok {
		return nil, false
	}
	pitr, _ := raw.(map[string]any)
	return pitr, true
}

// A pitr block without archive_interval_seconds follows the platform's
// interval, so an update sends an explicit null that clears a pin; a pinned
// value is sent as is; no pitr block sends nothing.
func TestManagedPostgresqlArchiveIntervalPinAndClear(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	created, d := l.create(databasePlan(f))
	mustSucceed(t, d)

	pinned := created
	pinned.Pitr = pitrObject(t, true, types.Int64Value(300))
	afterPin, d := l.update(pinned, created)
	mustSucceed(t, d)
	if pitr, _ := patchedPitr(t, f, 0); pitr["archive_interval_seconds"] != float64(300) {
		t.Errorf("PATCH pitr = %v, want the pinned 300", pitr)
	}

	cleared := afterPin
	cleared.Pitr = pitrObject(t, true, types.Int64Null())
	afterClear, d := l.update(cleared, afterPin)
	mustSucceed(t, d)
	pitr, _ := patchedPitr(t, f, 1)
	if v, sent := pitr["archive_interval_seconds"]; !sent || v != nil {
		t.Errorf("PATCH pitr = %v, want archive_interval_seconds: null", pitr)
	}
	if pitr["enabled"] != true {
		t.Errorf("PATCH pitr = %v, want enabled kept", pitr)
	}
	if !afterClear.Pitr.Equal(cleared.Pitr) {
		t.Errorf("pitr = %v, want %v", afterClear.Pitr, cleared.Pitr)
	}

	unmanaged := afterClear
	unmanaged.Pitr = types.ObjectNull(pitrType().AttrTypes)
	_, d = l.update(unmanaged, afterClear)
	mustSucceed(t, d)
	if pitr, sent := patchedPitr(t, f, 2); sent {
		t.Errorf("PATCH carries pitr %v although the configuration has none", pitr)
	}
}

func TestManagedPostgresqlEngines(t *testing.T) {
	s := resourceSchema(t, NewManagedPostgresqlResource()).Schema
	cfg := nullConfig(t, s, nil)
	for _, engine := range []string{"postgresql", "postgis"} {
		if d := validateString(t, s, cfg, path.Root("engine"), types.StringValue(engine)); d.HasError() {
			t.Errorf("engine %q was refused: %v", engine, d)
		}
	}
	if d := validateString(t, s, cfg, path.Root("engine"), types.StringValue("timescaledb")); !d.HasError() {
		t.Error("engine timescaledb was accepted")
	}
}

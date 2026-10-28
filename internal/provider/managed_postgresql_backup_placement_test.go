// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeDatabases serves one ready database, created by the first POST.
func fakeDatabases(t *testing.T) (*fakeAPI, *managedPostgresqlResource) {
	f, pd := newFakeAPI(t)
	id := uuid.New()
	db := console.ManagedPostgresqlResponse{
		Id: id, HarborId: f.env, OrganizationId: f.org, Name: "orders", Slug: "orders", DatabaseName: "orders",
		Engine: "postgresql", Version: "17", NodeTier: "small", StorageSize: "10Gi", Configuration: "standalone",
		BackupPolicy: "manual", Instances: 1, ReadyInstances: 1, Tags: []string{}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	f.on("POST", `/managed-postgresqls/crd`, func(string, []byte) (int, any) { return 201, db })
	f.on("GET", `/managed-postgresqls/[^/]+/crd`, func(string, []byte) (int, any) {
		return 200, console.ManagedPostgresqlCrdSpecResponse{Engine: "postgresql", Instances: 1, StorageSize: "10Gi", Version: "17"}
	})
	f.on("GET", `/managed-postgresqls/[^/]+`, func(string, []byte) (int, any) { return 200, db })
	f.on("PATCH", `/managed-postgresqls/[^/]+/crd`, func(string, []byte) (int, any) { return 200, nil })
	return f, &managedPostgresqlResource{p: pd}
}

func databasePlan(f *fakeAPI) managedPostgresqlModel {
	return managedPostgresqlModel{
		Env: types.StringValue(f.env.String()), Name: types.StringValue("orders"), DatabaseName: types.StringValue("orders"),
		ExposeToInternet: types.BoolValue(false), Tags: types.ListNull(types.StringType),
		Pitr: types.ObjectNull(pitrType().AttrTypes), Restore: types.ObjectNull(restoreType().AttrTypes),
	}
}

// The placement reaches the create request and, since no view reports it, is
// carried through every read.
func TestManagedPostgresqlCreateWithBackupPlacement(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	plan := databasePlan(f)
	plan.BackupPlacement = types.StringValue("platform")

	got, d := l.create(plan)
	mustSucceed(t, d)
	if body := f.calls("POST", `/crd`)[0].body; body["backup_placement"] != "platform" {
		t.Errorf("create body = %v", body)
	}
	if got.BackupPlacement.ValueString() != "platform" {
		t.Errorf("backup_placement = %v", got.BackupPlacement)
	}
	read, _, d := l.read(got)
	mustSucceed(t, d)
	if read.BackupPlacement.ValueString() != "platform" {
		t.Errorf("read backup_placement = %v, want it carried", read.BackupPlacement)
	}
}

// A database without one never sends it.
func TestManagedPostgresqlCreateWithoutBackupPlacement(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	_, d := l.create(databasePlan(f))
	mustSucceed(t, d)
	if _, ok := f.calls("POST", `/crd`)[0].body["backup_placement"]; ok {
		t.Error("create body carries backup_placement, which the config does not set")
	}
}

// Setting a placement patches it in place; an unchanged one is not re-sent, and
// one removed from the configuration is left as it is, since the API cannot
// unset it.
func TestManagedPostgresqlUpdateBackupPlacement(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	created, d := l.create(databasePlan(f))
	mustSucceed(t, d)

	next := created
	next.BackupPlacement = types.StringValue("platform")
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if body := f.calls("PATCH", `/crd`)[0].body; body["backup_placement"] != "platform" {
		t.Errorf("PATCH = %v, want backup_placement", body)
	}
	if updated.BackupPlacement.ValueString() != "platform" {
		t.Errorf("backup_placement = %v", updated.BackupPlacement)
	}

	next = updated
	next.Description = types.StringValue("orders database")
	_, d = l.update(next, updated)
	mustSucceed(t, d)
	if _, ok := f.calls("PATCH", `/crd`)[1].body["backup_placement"]; ok {
		t.Error("an unchanged backup_placement was re-sent")
	}

	next = updated
	next.BackupPlacement = types.StringNull()
	removed, d := l.update(next, updated)
	mustSucceed(t, d)
	if _, ok := f.calls("PATCH", `/crd`)[2].body["backup_placement"]; ok {
		t.Error("a removed backup_placement was sent")
	}
	if !removed.BackupPlacement.IsNull() {
		t.Errorf("backup_placement = %v, want null", removed.BackupPlacement)
	}
}

// The platform refuses a placement next to a named target.
func TestManagedPostgresqlBackupPlacementConflictsWithTarget(t *testing.T) {
	s := resourceSchema(t, &managedPostgresqlResource{}).Schema
	cfg := nullConfig(t, s, map[string]tftypes.Value{
		"backup_target_id": tftypes.NewValue(tftypes.String, uuid.NewString()),
		"backup_placement": tftypes.NewValue(tftypes.String, "platform"),
	})
	if d := validateString(t, s, cfg, path.Root("backup_target_id"), types.StringValue(uuid.NewString())); !d.HasError() {
		t.Error("backup_placement next to backup_target_id was accepted")
	}
	if d := validateString(t, s, cfg, path.Root("backup_placement"), types.StringValue("cluster")); !d.HasError() {
		t.Error("an unknown placement was accepted")
	}
}

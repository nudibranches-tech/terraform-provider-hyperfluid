// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccManagedPostgresql covers a cluster + a user: create → read → update
// (storage_capacity) → import (both). Skipped unless HYPERFLUID_CREDENTIALS is
// set (testAccPreCheck), so it is a no-op in CI without cluster access.
func TestAccManagedPostgresql(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccManagedPostgresqlConfig(1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "name", "tf-acc-pg"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "storage_capacity", "1"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "node_tier", "nano"),
					// omitted from config above → asserts the default is false (private).
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "expose_to_internet", "false"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "backup_policy", "manual"),
					resource.TestCheckResourceAttrSet("hyperfluid_managed_postgresql.db", "write_endpoint"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql_user.editor", "username", "app_editor"),
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql_user.editor", "permission_level", "editor"),
				),
			},
			{
				// in-place storage growth
				Config: testAccManagedPostgresqlConfig(2),
				Check:  resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.db", "storage_capacity", "2"),
			},
			{
				ResourceName:      "hyperfluid_managed_postgresql.db",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "hyperfluid_managed_postgresql_user.editor",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					cluster := s.RootModule().Resources["hyperfluid_managed_postgresql.db"]
					user := s.RootModule().Resources["hyperfluid_managed_postgresql_user.editor"]
					return fmt.Sprintf("%s/%s", cluster.Primary.ID, user.Primary.ID), nil
				},
			},
		},
	})
}

func testAccManagedPostgresqlConfig(storageGB int) string {
	return fmt.Sprintf(`
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_managed_postgresql" "db" {
  env           = data.hyperfluid_env.default.id
  name             = "tf-acc-pg"
  database_name    = "appdb"
  engine           = "postgresql"
  version          = "17"
  node_tier        = "nano"
  storage_capacity = %d
  configuration    = "standalone"
}

resource "hyperfluid_managed_postgresql_user" "editor" {
  managed_postgresql = hyperfluid_managed_postgresql.db.id
  username           = "app_editor"
  permission_level   = "editor"
}
`, storageGB)
}

// TestManagedPostgresqlSchema checks the rules the framework only enforces when
// Terraform calls the provider — notably that every child of the write-only
// `restore` is itself write-only.
func TestManagedPostgresqlSchema(t *testing.T) {
	assertSchemaImplementation(t, NewManagedPostgresqlResource())
}

// TestRestoreIsWriteOnly pins the attribute shape the rest of the design rests
// on: `restore` never reaches state, so it never produces a diff and never
// needs a plan modifier.
func TestRestoreIsWriteOnly(t *testing.T) {
	s := resourceSchema(t, NewManagedPostgresqlResource())
	for _, p := range []path.Path{
		path.Root("restore"),
		path.Root("restore").AtName("backup_id"),
		path.Root("restore").AtName("source_instance_id"),
		path.Root("restore").AtName("target_time"),
		path.Root("restore").AtName("exclusive"),
	} {
		attr, d := s.AttributeAtPath(context.Background(), p)
		if d.HasError() {
			t.Fatalf("attribute %s: %v", p, d)
		}
		if !attr.IsWriteOnly() {
			t.Errorf("%s is not write-only", p)
		}
		if attr.IsComputed() {
			t.Errorf("%s is computed, which a write-only attribute may not be", p)
		}
	}
}

// TestRestoreSourceValidators covers the three combinations the platform will
// not do what the config asks: no source, both sources, and the two attributes
// that only mean something alongside another.
func TestRestoreSourceValidators(t *testing.T) {
	s := resourceSchema(t, NewManagedPostgresqlResource())
	backupPath := path.Root("restore").AtName("backup_id")
	timePath := path.Root("restore").AtName("target_time")
	exclusivePath := path.Root("restore").AtName("exclusive")

	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	restore := func(members map[string]tftypes.Value) tfsdk.Config {
		return nullConfig(t, s, map[string]tftypes.Value{
			"restore": objectValue(t, s, path.Root("restore"), members),
		})
	}

	t.Run("neither source is set", func(t *testing.T) {
		cfg := restore(nil)
		if d := validateString(t, s, cfg, backupPath, types.StringNull()); !d.HasError() {
			t.Error("a restore naming no source was accepted")
		}
	})

	t.Run("both sources are set", func(t *testing.T) {
		cfg := restore(map[string]tftypes.Value{
			"backup_id":          str("6f3f9f7e-0e3c-4f2f-9a2f-0f5f1f2f3f4f"),
			"source_instance_id": str("11111111-2222-3333-4444-555555555555"),
		})
		if d := validateString(t, s, cfg, backupPath, types.StringValue("6f3f9f7e-0e3c-4f2f-9a2f-0f5f1f2f3f4f")); !d.HasError() {
			t.Error("a restore naming both sources was accepted")
		}
	})

	t.Run("one source is set", func(t *testing.T) {
		cfg := restore(map[string]tftypes.Value{"backup_id": str("6f3f9f7e-0e3c-4f2f-9a2f-0f5f1f2f3f4f")})
		if d := validateString(t, s, cfg, backupPath, types.StringValue("6f3f9f7e-0e3c-4f2f-9a2f-0f5f1f2f3f4f")); d.HasError() {
			t.Errorf("a restore from one backup was refused: %v", d)
		}
	})

	t.Run("target_time pinned to a backup", func(t *testing.T) {
		cfg := restore(map[string]tftypes.Value{
			"backup_id":   str("6f3f9f7e-0e3c-4f2f-9a2f-0f5f1f2f3f4f"),
			"target_time": str("2026-09-18T09:00:00Z"),
		})
		if d := validateString(t, s, cfg, timePath, types.StringValue("2026-09-18T09:00:00Z")); !d.HasError() {
			t.Error("target_time was accepted without source_instance_id")
		}
	})

	t.Run("target_time on a continuous archive", func(t *testing.T) {
		cfg := restore(map[string]tftypes.Value{
			"source_instance_id": str("11111111-2222-3333-4444-555555555555"),
			"target_time":        str("2026-09-18T09:00:00Z"),
		})
		if d := validateString(t, s, cfg, timePath, types.StringValue("2026-09-18T09:00:00Z")); d.HasError() {
			t.Errorf("target_time alongside source_instance_id was refused: %v", d)
		}
	})

	// The platform silently drops `exclusive` without a target_time, so the
	// refusal has to come from here or nowhere.
	t.Run("exclusive without a target_time", func(t *testing.T) {
		cfg := restore(map[string]tftypes.Value{
			"source_instance_id": str("11111111-2222-3333-4444-555555555555"),
			"exclusive":          tftypes.NewValue(tftypes.Bool, true),
		})
		if d := validateBool(t, s, cfg, exclusivePath, types.BoolValue(true)); !d.HasError() {
			t.Error("exclusive was accepted without target_time")
		}
	})
}

// TestRestoreWoVersion covers the only handle Terraform has on a write-only
// block: the token that makes a changed `restore` visible, and that forces the
// replacement a restore needs (the platform cannot rewind a database in place).
func TestRestoreWoVersion(t *testing.T) {
	s := resourceSchema(t, NewManagedPostgresqlResource())
	p := path.Root("restore_wo_version")

	attr, d := s.AttributeAtPath(context.Background(), p)
	if d.HasError() {
		t.Fatalf("attribute %s: %v", p, d)
	}
	if attr.IsWriteOnly() || attr.IsComputed() {
		t.Error("restore_wo_version must reach state, or it cannot be compared across plans")
	}
	if !requiresReplaceString(t, s, p, types.StringValue("1"), types.StringValue("2")) {
		t.Error("a changed restore_wo_version does not force replacement, so the restore never re-runs")
	}
	if requiresReplaceString(t, s, p, types.StringValue("1"), types.StringValue("1")) {
		t.Error("an unchanged restore_wo_version forces replacement")
	}

	t.Run("without a restore block", func(t *testing.T) {
		cfg := nullConfig(t, s, nil)
		if d := validateString(t, s, cfg, p, types.StringValue("1")); !d.HasError() {
			t.Error("restore_wo_version was accepted with nothing to restore")
		}
	})

	t.Run("alongside a restore block", func(t *testing.T) {
		cfg := nullConfig(t, s, map[string]tftypes.Value{
			"restore": objectValue(t, s, path.Root("restore"), map[string]tftypes.Value{
				"source_instance_id": tftypes.NewValue(tftypes.String, "11111111-2222-3333-4444-555555555555"),
			}),
		})
		if d := validateString(t, s, cfg, p, types.StringValue("1")); d.HasError() {
			t.Errorf("restore_wo_version alongside a restore was refused: %v", d)
		}
	})
}

func TestPitrArchiveIntervalBounds(t *testing.T) {
	s := resourceSchema(t, NewManagedPostgresqlResource())
	p := path.Root("pitr").AtName("archive_interval_seconds")
	cfg := nullConfig(t, s, nil)

	for _, seconds := range []int64{59, 0, 86401} {
		if d := validateInt64(t, s, cfg, p, types.Int64Value(seconds)); !d.HasError() {
			t.Errorf("archive_interval_seconds = %d was accepted", seconds)
		}
	}
	// 60 is the hard floor and 300 the recommended one: below the recommendation
	// is a legitimate choice, so only the hard floor may refuse.
	for _, seconds := range []int64{60, 120, 300, 86400} {
		if d := validateInt64(t, s, cfg, p, types.Int64Value(seconds)); d.HasError() {
			t.Errorf("archive_interval_seconds = %d was refused: %v", seconds, d)
		}
	}
}

func TestPitrRequestFrom(t *testing.T) {
	ctx := context.Background()

	t.Run("absent", func(t *testing.T) {
		got, d := pitrRequestFrom(ctx, types.ObjectNull(pitrType().AttrTypes))
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		if got != nil {
			t.Errorf("an absent pitr produced a body: %+v", got)
		}
	})

	t.Run("enabled without a pin", func(t *testing.T) {
		obj, d := types.ObjectValueFrom(ctx, pitrType().AttrTypes, pitrModel{
			Enabled:                types.BoolValue(true),
			ArchiveIntervalSeconds: types.Int64Null(),
		})
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		got, d := pitrRequestFrom(ctx, obj)
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		if !got.Enabled {
			t.Error("enabled was not carried through")
		}
		// Absent, not zero: the platform resolves its own interval at every
		// reconcile only while the field stays off the request.
		if got.ArchiveIntervalSeconds != nil {
			t.Errorf("an unpinned interval reached the request as %d", *got.ArchiveIntervalSeconds)
		}
	})

	t.Run("disabled with a pin", func(t *testing.T) {
		obj, d := types.ObjectValueFrom(ctx, pitrType().AttrTypes, pitrModel{
			Enabled:                types.BoolValue(false),
			ArchiveIntervalSeconds: types.Int64Value(600),
		})
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		got, d := pitrRequestFrom(ctx, obj)
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		if got.Enabled {
			t.Error("disabled was not carried through")
		}
		if got.ArchiveIntervalSeconds == nil || *got.ArchiveIntervalSeconds != 600 {
			t.Errorf("the pin was not carried through: %v", got.ArchiveIntervalSeconds)
		}
	})
}

func TestRestoreFrom(t *testing.T) {
	ctx := context.Background()
	object := func(t *testing.T, m restoreModel) types.Object {
		t.Helper()
		obj, d := types.ObjectValueFrom(ctx, restoreType().AttrTypes, m)
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		return obj
	}

	t.Run("absent", func(t *testing.T) {
		got, d := restoreFrom(ctx, types.ObjectNull(restoreType().AttrTypes))
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		if got != nil {
			t.Errorf("an absent restore produced a body: %+v", got)
		}
	})

	t.Run("from a backup", func(t *testing.T) {
		got, d := restoreFrom(ctx, object(t, restoreModel{
			BackupID:         types.StringValue("6f3f9f7e-0e3c-4f2f-9a2f-0f5f1f2f3f4f"),
			SourceInstanceID: types.StringNull(),
			TargetTime:       types.StringNull(),
			Exclusive:        types.BoolNull(),
		}))
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		if got.BackupId == nil || got.BackupId.String() != "6f3f9f7e-0e3c-4f2f-9a2f-0f5f1f2f3f4f" {
			t.Errorf("backup_id was not parsed: %v", got.BackupId)
		}
		if got.SourceInstanceId != nil || got.TargetTime != nil || got.Exclusive != nil {
			t.Errorf("unset fields reached the request: %+v", got)
		}
	})

	t.Run("to a point in time", func(t *testing.T) {
		got, d := restoreFrom(ctx, object(t, restoreModel{
			BackupID:         types.StringNull(),
			SourceInstanceID: types.StringValue("11111111-2222-3333-4444-555555555555"),
			TargetTime:       types.StringValue("2026-09-18T09:00:00Z"),
			Exclusive:        types.BoolValue(true),
		}))
		if d.HasError() {
			t.Fatalf("unexpected diagnostics: %v", d)
		}
		if got.SourceInstanceId == nil || got.SourceInstanceId.String() != "11111111-2222-3333-4444-555555555555" {
			t.Errorf("source_instance_id was not parsed: %v", got.SourceInstanceId)
		}
		if got.TargetTime == nil || !got.TargetTime.Equal(time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("target_time was not parsed: %v", got.TargetTime)
		}
		if got.Exclusive == nil || !*got.Exclusive {
			t.Errorf("exclusive was not carried through: %v", got.Exclusive)
		}
	})

	t.Run("unparseable id", func(t *testing.T) {
		_, d := restoreFrom(ctx, object(t, restoreModel{
			BackupID:         types.StringValue("not-a-uuid"),
			SourceInstanceID: types.StringNull(),
			TargetTime:       types.StringNull(),
			Exclusive:        types.BoolNull(),
		}))
		if !d.HasError() {
			t.Error("an unparseable backup_id was accepted")
		}
	})

	t.Run("unparseable timestamp", func(t *testing.T) {
		_, d := restoreFrom(ctx, object(t, restoreModel{
			BackupID:         types.StringNull(),
			SourceInstanceID: types.StringValue("11111111-2222-3333-4444-555555555555"),
			TargetTime:       types.StringValue("yesterday"),
			Exclusive:        types.BoolNull(),
		}))
		if !d.HasError() {
			t.Error("an unparseable target_time was accepted")
		}
	})
}

// TestAccManagedPostgresqlPitr covers the recovery policy end to end: a cluster
// created with PITR on, then the interval pinned in place. Needs a real backup
// target (HYPERFLUID_TEST_S3_*) because the platform refuses the policy without
// one; skipped otherwise. Each step's implicit follow-up plan is the real
// assertion — `pitr` is carried from state, so a mismatch shows up as drift.
func TestAccManagedPostgresqlPitr(t *testing.T) {
	endpoint := os.Getenv("HYPERFLUID_TEST_S3_ENDPOINT")
	akSecret := os.Getenv("HYPERFLUID_TEST_S3_ACCESS_KEY_SECRET")
	skSecret := os.Getenv("HYPERFLUID_TEST_S3_SECRET_KEY_SECRET")
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if endpoint == "" || akSecret == "" || skSecret == "" {
				t.Skip("HYPERFLUID_TEST_S3_* not set; skipping managed_postgresql PITR acceptance test")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccManagedPostgresqlPitrConfig(endpoint, akSecret, skSecret, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_managed_postgresql.pitr", "pitr.enabled", "true"),
					// Unpinned in the config, so the platform resolves the interval
					// and only the top-level attribute carries a number.
					resource.TestCheckNoResourceAttr("hyperfluid_managed_postgresql.pitr", "pitr.archive_interval_seconds"),
					resource.TestCheckResourceAttrSet("hyperfluid_managed_postgresql.pitr", "archive_interval_seconds"),
				),
			},
			{
				Config: testAccManagedPostgresqlPitrConfig(endpoint, akSecret, skSecret, "    archive_interval_seconds = 600"),
				Check: resource.TestCheckResourceAttr(
					"hyperfluid_managed_postgresql.pitr", "pitr.archive_interval_seconds", "600"),
			},
		},
	})
}

func testAccManagedPostgresqlPitrConfig(endpoint, akSecret, skSecret, interval string) string {
	return fmt.Sprintf(`
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_backup_target" "pitr" {
  env                           = data.hyperfluid_env.default.id
  name                          = "tf-acc-pitr-bt"
  endpoint_url                  = %q
  destination_path              = "s3://default-backup/tf-acc-pitr/"
  access_key_secret_name        = %q
  secret_access_key_secret_name = %q
  insecure                      = true
  retention_days                = 14
}

resource "hyperfluid_managed_postgresql" "pitr" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-pg-pitr"
  database_name    = "appdb"
  node_tier        = "nano"
  storage_capacity = 1
  backup_target_id = hyperfluid_backup_target.pitr.id

  pitr = {
    enabled = true
%s
  }
}
`, endpoint, akSecret, skSecret, interval)
}

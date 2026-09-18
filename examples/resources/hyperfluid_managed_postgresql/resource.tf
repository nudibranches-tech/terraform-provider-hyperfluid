data "hyperfluid_env" "default" {
  name = "default"
}

# Where the write-ahead log and the base backups ship. A recovery policy needs
# one, so the database below depends on it explicitly.
data "hyperfluid_backup_target" "offsite" {
  env  = data.hyperfluid_env.default.id
  name = "offsite"
}

resource "hyperfluid_managed_postgresql" "db" {
  env              = data.hyperfluid_env.default.id
  name             = "appdb"
  database_name    = "appdb"
  engine           = "postgresql"
  version          = "17"
  node_tier        = "nano"
  storage_capacity = 5
  configuration    = "standalone"

  # Defaults to false (reachable only in-cluster). Set true to publish an
  # external NodePort endpoint.
  expose_to_internet = true

  backup_target_id = data.hyperfluid_backup_target.offsite.id

  # A daily base backup. Recovery replays the write-ahead log forward from one,
  # so a database that only archives WAL has nothing to restore from.
  backup_policy = "automated"

  # Point-in-time recovery. Leave archive_interval_seconds out and the platform
  # resolves its own interval, so a platform change reaches this database; pin it
  # to hold a tighter recovery point objective, at one 16 MiB upload per interval.
  pitr = {
    enabled                  = true
    archive_interval_seconds = 300
  }
}

# A second database recovered from the first one's archive, as it stood at a
# chosen moment. restore is read when the database is created and never again:
# the platform cannot rewind a database in place.
resource "hyperfluid_managed_postgresql" "db_recovered" {
  env           = data.hyperfluid_env.default.id
  name          = "appdb-recovered"
  database_name = "appdb"

  # A restore refuses any engine or version but the source's, and an omitted one
  # is read as postgresql / 17 rather than inherited.
  engine           = "postgresql"
  version          = "17"
  node_tier        = "nano"
  storage_capacity = 5

  restore = {
    source_instance_id = hyperfluid_managed_postgresql.db.id
    target_time        = "2026-09-18T09:00:00Z"
  }

  # restore is write-only, so it never reaches state and Terraform cannot see it
  # change. Change this token alongside it to ask for the restore again; that
  # replaces the database, which is the only moment a restore happens.
  restore_wo_version = "1"
}

output "write_endpoint" {
  value = hyperfluid_managed_postgresql.db.write_endpoint
}

# The bounds a restore can target: the earliest point the backup catalog still
# holds, and the base backup recovery replays forward from.
output "recovery_window" {
  value = {
    first_recoverability_point  = hyperfluid_managed_postgresql.db.first_recoverability_point
    last_successful_backup_time = hyperfluid_managed_postgresql.db.last_successful_backup_time
    archive_interval_seconds    = hyperfluid_managed_postgresql.db.archive_interval_seconds
  }
}

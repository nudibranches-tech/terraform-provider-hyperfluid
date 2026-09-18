## 0.1.0 (Unreleased)

FEATURES:

* resource/hyperfluid_managed_postgresql: point-in-time recovery — `pitr` opts a database in and pins how tight its recovery point objective is, `restore` bootstraps a new database from a backup or from another database's continuous archive at a chosen moment (paired with `restore_wo_version`, which makes a changed restore visible to Terraform), and `archive_interval_seconds`, `first_recoverability_point`, `last_successful_backup_time` and `last_failed_backup_time` report the window a restore can target
* data-source/hyperfluid_managed_postgresql: the same four recovery-window attributes
* resource/hyperfluid_backup_target: `retention_days` sets how long base backups and WAL archives are kept, applied in place
* data-source/hyperfluid_backup_target: `retention_days`

BUG FIXES:

* resource/hyperfluid_managed_postgresql: a cluster created with `backup_target_id` no longer fails the apply with an inconsistent-result error — the id is carried through a read, which no API view reports

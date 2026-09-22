## 0.1.0 (Unreleased)

FEATURES:

* **New Resource:** `hyperfluid_airflow` — a managed Apache Airflow 3 environment, with its metadata PostgreSQL cluster, DAG bucket, task-pod egress grants and `airflow.cfg` overrides.
* **New Resource:** `hyperfluid_airflow_connection` — a managed Airflow connection to a PostgreSQL cluster, a bucket, or a catalog on a Trino Data Dock; the platform puts the credential in place and rotates it, so none ever reaches Terraform state. A Trino connection names `trino_ref` plus a required `catalog` — the session default for unqualified table names, not a scope — and task pods reach the dock at its in-cluster address; it carries the environment's own service account, so `permission_level` does not apply to it and is refused at plan time.
* **New Data Source:** `hyperfluid_airflow` — look up an existing Airflow environment by name within a harbor.
* **New Data Source:** `hyperfluid_airflow_connection` — look up a managed connection by the `conn_id` a DAG asks for.

ENHANCEMENTS:

* resource/hyperfluid_managed_postgresql: point-in-time recovery — `pitr` opts a database in and pins how tight its recovery point objective is, `restore` bootstraps a new database from a backup or from another database's continuous archive at a chosen moment (paired with `restore_wo_version`, which makes a changed restore visible to Terraform), and `archive_interval_seconds`, `first_recoverability_point`, `last_successful_backup_time` and `last_failed_backup_time` report the window a restore can target
* data-source/hyperfluid_managed_postgresql: the same four recovery-window attributes
* resource/hyperfluid_backup_target: `retention_days` sets how long base backups and WAL archives are kept, applied in place
* data-source/hyperfluid_backup_target: `retention_days`
* `hyperfluid_service_link` accepts `Airflow` and `Pipeline` as a `consumer` kind, and refuses both as a `target` kind at plan time — matching the API, which treats each as consumer-only because both reach their targets and neither is ever reached.

BUG FIXES:

* resource/hyperfluid_managed_postgresql: a cluster created with `backup_target_id` no longer fails the apply with an inconsistent-result error — the id is carried through a read, which no API view reports
* `make fetch-spec` no longer vendors an empty spec. The console spec passed 1 MiB (~2.1 MB), above which GitHub's contents API inlines no base64 and answers `"content": ""`; `base64 -d` of nothing succeeds, so the fetch wrote a zero-byte file and exited 0. It now streams the blob with the raw media type and refuses any result that carries no `openapi` version or no paths, so a failed fetch is a red build rather than a silently emptied client.

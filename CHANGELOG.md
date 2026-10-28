## Unreleased

DEPRECATIONS:

* data-source/hyperfluid_env: deprecated in favour of `hyperfluid_environment`, which takes the same arguments. Renaming `data "hyperfluid_env"` to `data "hyperfluid_environment"` plans no change, and `hyperfluid_env` keeps working.

FEATURES:

* **New Resource:** `hyperfluid_environment` — create an environment (`name`, `slug`), imported by its slug. Both force a new environment. Destroying an environment deletes everything in it, so `deletion_protection` (default `true`, Terraform-only) refuses both a destroy and a replacement until it is set to `false` and applied. Creating one needs the `harbor:create` permission and room in the organization's environment quota; the provider names whichever is missing.
* **New Data Source:** `hyperfluid_environment` — look up an environment by slug or name, to get the id other resources take as `env`.
* **New Resource:** `hyperfluid_s3_access_key` — a long-lived S3 key of an environment for an existing service account (`service_account_id`, referenced through the `hyperfluid_service_account` data source). The secret is returned once and stored, sensitive, in state. Revoking a key also revokes the older keys of the same service account, which is reported as a warning. Scoped keys, which the console issues together with a dedicated service account, are created in the console: Terraform never creates service accounts.
* **New Resource:** `hyperfluid_role` — a custom role and its `permissions` (keys such as `secret:read`). The system roles `owner`, `editor` and `viewer` cannot be created, changed or imported.
* **New Resource:** `hyperfluid_group` — a group with its `members`, `roles` and `grants`. Destroying it deletes the group.
* **New Resource:** `hyperfluid_service_account_access` — the `roles`, `grants` and `attributes` of a service account created in the console or with `hfctl`.
* **New Resource:** `hyperfluid_user_access` — the `roles` and `grants` of a user, including the organization-wide roles given at sign-up or by invitation. Removing the last owner is refused.
* IAM lists (`permissions`, `members`, `roles`, `grants`, `attributes`) are sets. A list left out of the configuration is not managed; once set, it is authoritative, and removing it clears it. Destroying an access resource clears the lists it manages and keeps the principal. A role or grant anchored with `resource_uuid` or `resource_type` needs an exact `scope_path`, and unknown permission keys fail the plan. Import ids are the role, group, service account or user id, and no list is managed after import until the configuration sets it.
* **New Data Source:** `hyperfluid_permissions` — the permission catalog, optionally narrowed to one `resource_type`.
* **New Data Source:** `hyperfluid_group` — look up a group by name, with its `members`.
* **New Data Source:** `hyperfluid_service_account` — look up a service account by `client_id` or display `name`; its secret is never returned.
* **New Data Source:** `hyperfluid_role` — look up a role by name, system roles included, with the permission keys it holds.
* resource/hyperfluid_container_app: `env_vars` (an ordered list of `{ name, value, secret }`, each variable set from exactly one of a plain `value` and `secret`, the name of an organization-wide `hyperfluid_secret` whose value never lands in state; a value can reference an earlier plain variable as `$(NAME)`, and a reference to a secret-backed one is refused at plan time since those are set after every plain variable), `image_pull_secrets`, `command`, `args` and `init_containers` (at most 4; each runs the app's own image unless it sets `image_repository` and `image_tag`, and inherits the app's environment and volumes unless `inherit_env` or `mount_persistence` is false). A list left out of the configuration is not managed: it reports what the app has and an update never sends it, so an init container provisioned by an app template survives an apply. Once applied, removing a list from the configuration clears it; `[]` clears it explicitly. Init container names, image pairs, blank executables, duplicate variables and private secret references (under `users/`) are refused at plan time. Plain `env_vars` values are stored in the app's spec and in state: put credentials in a secret.
* resource/hyperfluid_container_app: a non-empty `command` or `args`, on the app or on an init container, needs the organization's container command override feature; the platform refuses it otherwise, and the provider reports that by name. An app keeps updating after the feature is turned off: an unchanged main command is never re-sent, and the platform accepts an init container that keeps its name, command and args. Clearing a command is always allowed.
* resource/hyperfluid_container_app: `file_mounts` (inline `content` or an organization-wide `secret_name`; 8 KiB per file, 32 KiB in all), `custom_domains` (`fqdn`, `tls_mode`, `tls_secret_name`) with the computed `custom_domain_statuses`, `autoscaling` (conflicts with `replicas`; scaling to zero or on HTTP concurrency needs `expose_to_internet` and an HTTP port), `metrics` (`port` names an entry of `ports`), `custom_resources` (an explicit `cpu_millis` / `memory_mib`, conflicting with `resource_tier`) and `resource_mode`. `file_mounts`, `custom_domains`, `autoscaling` and `metrics` follow the same rule as the lists above: left out they are not managed, and once applied, removing one clears it (`autoscaling` and `metrics` through `clear_autoscaling` / `clear_metrics`). The computed `image_digest` and `tag_is_floating` report the pinned digest and whether the tag can be re-pushed upstream. Terraform now waits for an autoscaled app to converge on the count its autoscaler chose.
* data-source/hyperfluid_container_app: `env_vars`, `image_pull_secrets`, `command`, `args`, `init_containers`, `file_mounts`, `custom_domains`, `custom_domain_statuses`, `autoscaling`, `metrics`, `resource_mode`, `image_digest` and `tag_is_floating`.
* **New Resource:** `hyperfluid_alert_channel` — a notification channel alerts are delivered through: `email` with its `recipients`, or a Slack or Discord webhook. The webhook `url` is write-only and sensitive, since the platform only returns it masked (`url_masked`); rotate it with `url_wo_version`.
* **New Resource:** `hyperfluid_alert_rule` — an alert on one Monitoring panel of one resource, with a warning and/or a critical tier, a `comparator` and a hold time (`for_seconds`).
* **New Resource:** `hyperfluid_alert_threshold` — the organization's override of a capacity signal's thresholds (`pvc_disk`, `bucket_quota`). Destroying it resets the signal to the platform defaults; a signal reset outside Terraform drops from state. Its import id is the signal.
* **New Data Source:** `hyperfluid_alert_channel` — look up an alert channel by name.
* Alerting needs the organization's `alerting_enabled` feature; without it the provider reports the feature by name.
* **New Resource:** `hyperfluid_sql_engine_cluster` — a SQL engine cluster (Trino) in an environment: `worker_replicas`, the coordinator and worker sizes, the query limits, `worker_auto_scaling`, `description` and `refresh_interval`, all changed in place. A tuning attribute left out is never sent, so the platform keeps resolving its default. `expose_to_internet` is only managed when set. Imported by id.
* **New Data Source:** `hyperfluid_sql_engine_cluster` — look up a SQL engine cluster by name within an environment.
* **New Resource:** `hyperfluid_sql_engine_catalog` — a catalog of a SQL engine cluster, one resource for every `kind`: `iceberg` (with `bucket_name`), `postgresql` (with exactly one of `managed_postgresql_user_id` and `connection_secret`), `mysql`, `mariadb` and `clickhouse` (with `connection_secret`), and the `tpch`, `tpcds` and `faker` demo datasets. A credential source that does not fit the kind is refused at plan time. Catalogs cannot change in place. Imported as `<cluster_id>/<name>`; the credential source is not recovered, and the first apply records it from the configuration.
* **New Resource:** `hyperfluid_pipeline` — a data pipeline declared as the body `hfctl pipelines create --spec` takes: `type` and a `spec` JSON object, while the resource owns the `pipeline` block (`name`, `env`, `schedule`). `harbor_bucket` references default to `env`. `suspended` and `max_duration_minutes` change in place; any other change replaces the pipeline. Runs stay out of Terraform. Imported by id; `type`, `spec` and `max_duration_minutes` are not recovered, and the first apply records them from the configuration.
* **New Resource:** `hyperfluid_nextflow` — the Nextflow deployment of an environment: `buckets`, `egress`, `config`, `runtime_image`, `head`, `task_quota`, `storage` and `scm`. Settings left out are resolved by the platform. Runs are launched with `hfctl`. Needs the `nextflow_enabled` feature.
* **New Resource:** `hyperfluid_compute_dock` — a Slurm cluster in an environment, with its `partitions`, `/home` volume, login and REST nodes, and `auto_suspend_idle_minutes`. Partitions, `enabled`, `version`, `home_size` and auto-suspend change in place. Needs the `compute_docks_enabled` feature.
* **New Resource:** `hyperfluid_agent` — a Sapience AI agent: its `inference`, prompts, `tools`, `data_sources`, `service_account_id`, `interfaces`, `mail` and `desired_state`. A capability is an agent with a `speciality` and a `subagent_of`. Needs the `sapience_agent_enabled` feature.
* **New Data Source:** `hyperfluid_nextflow`, `hyperfluid_compute_dock` and `hyperfluid_agent` — look one up by name within an environment.
* **New Resource:** `hyperfluid_virtual_machine` — a virtual machine in an environment, booted from a catalog image (`image_source_ref`, `image_key`) with its `cpu`, `memory`, `disk` and an optional cloud-init `user_data_secret_name`. `running`, `description` and `tags` change in place, and `install_media_removed` detaches an installer's media once the guest is installed; any other change replaces the machine. Terraform waits for `Running` (or `Stopped`). Imported by id. Needs the `virtual_machines_enabled` feature.
* **New Resource:** `hyperfluid_virtual_machine_user` — a guest OS account with a generated password, an SSH public key read from a `hyperfluid_secret` (`ssh_public_key_secret_name`), or both. No credential lands in state. Terraform waits for the guest agent to connect and apply it. Every change replaces the account. Imported as `<vm_id>/<user_id>`.
* **New Data Source:** `hyperfluid_virtual_machine` — look up a virtual machine by name within an environment.
* **New Data Source:** `hyperfluid_virtual_machine_images` — the images an environment can boot, to pick `image_source_ref` and `image_key` from.
* When the organization does not have the feature a resource needs, the error names it.

ENHANCEMENTS:

* resource/hyperfluid_ai_api_key: `max_llm_rpm` and `max_llm_tpm`, applied in place; removing one clears it. `platform_max_llm_rpm/_tpm` and `effective_max_llm_rpm/_tpm` report the platform's cap and what the gateway enforces.
* data-source/hyperfluid_ai_api_key: the same six ceilings.
* resource/hyperfluid_managed_postgresql: `backup_placement = "platform"` leaves the backup target to the platform; it conflicts with `backup_target_id` and applies in place. No view reports it, so it is carried from state.
* resource/hyperfluid_backup_target: `bucket_name` backs up into a bucket of the environment, and the external endpoint attributes are then computed from what the platform resolved. Switching between the two replaces the target.
* resource/hyperfluid_key_value_cache, data-source/hyperfluid_key_value_cache: `resources` (`requests`/`limits` `cpu`/`memory`); removing it clears them.

BUG FIXES:

* resource/hyperfluid_container_app: removing `health_check_path` removes the app's health check. The update used to leave it in place, so the apply ended in an inconsistent result. `health_check_port` now requires `health_check_path`, since the platform ignores a port alone.

## 0.10.0 (October 20, 2026)

DEPRECATIONS:

* resource/hyperfluid_container_app, data-source/hyperfluid_container_app: `name` is deprecated in favour of `slug`; it is the same value. Renaming `name` to `slug` in a configuration plans no change, and `name` keeps reporting the slug for references that read it.

FEATURES:

* resource/hyperfluid_container_app: `image_source { git { ... } }` deploys the image a Git repository declares in `hyperfluid.toml`, instead of a literal `image_repository` / `image_tag` (the blocks conflict). `provider`, `repository` and `credential` are required; `base_url`, `branch` or `tag_pattern`, `path`, `container`, `interval` and `alert_on_sync_failure` are optional, and an omitted one is never written into the app, so the platform's defaults can change underneath it. `credential` names a `scm_credential` secret and is required even for a public repository: unauthenticated provider API calls are rate-limited per source IP, which every app of the cluster shares, while a token gets its own budget. What the platform resolved is reported by the computed `resolved_image`, `resolved_ref`, `revision`, `last_synced_at` and `sync_error`, which Terraform never plans a change for: a release made through Git is not drift. `image_repository` and `image_tag` are no longer required when `image_source` is set. Removing the block detaches the source and applies the literal image.
* resource/hyperfluid_container_app: `image_source.git.sync_policy` is `auto` (every change found in Git rolls out) or `manual` (a change is only reported until someone runs Sync in the console or `hfctl apps sync`; the first image of a new source still rolls out). Omitted, it is never written into the app and the platform treats it as `auto`, or as `manual` while the organization has automatic sync turned off, in which case an explicit `auto` is refused. Check now and Sync are actions and have no Terraform equivalent.
* data-source/hyperfluid_container_app: `image_source` (with `git.sync_policy`), `resolved_image`, `resolved_ref`, `revision`, `last_synced_at` and `sync_error`; `image_repository` and `image_tag` report the resolved image of a Git-sourced app, or null before its first check.
* resource/hyperfluid_secret: `secret_type = "scm_credential"` creates a Git credential, with a write-only `value` of `{ provider, username?, base_url?, token }` rotated through `value_wo_version` like any other secret. `provider` is `github`, `gitlab` or `forgejo`.

ENHANCEMENTS:

* resource/hyperfluid_container_app: `slug` sets the app's identifier, and is derived from `display_name` when omitted. `display_name` (the app's name, shown in the console and not unique), `description` and `tags` are new and change in place.
* data-source/hyperfluid_container_app: looks an app up by `slug`, and reports `display_name`, `description` and `tags`.
* The `hyperfluid_airflow` and `hyperfluid_airflow_connection` descriptions call a harbor a Hyperfluid environment, as the product does, and errors from every resource read `invalid environment` and `list environments` instead of naming a harbor.

BUG FIXES:

* resource/hyperfluid_container_app, data-source/hyperfluid_container_app: apps are resolved by slug, their only unique key, instead of by display name. Since display names may repeat within an environment, matching one could bind a resource after create, or a data source lookup, to another app. `name` is the app's slug and the data source's `name` now only matches a slug.

## 0.9.0 (September 22, 2026)

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

## 0.1.0 (Unreleased)

FEATURES:

* **New Resource:** `hyperfluid_airflow` — a managed Apache Airflow 3 environment, with its metadata PostgreSQL cluster, DAG bucket, task-pod egress grants and `airflow.cfg` overrides.
* **New Resource:** `hyperfluid_airflow_connection` — a managed Airflow connection to a PostgreSQL cluster, a bucket, or a catalog on a Trino Data Dock; the platform puts the credential in place and rotates it, so none ever reaches Terraform state. A Trino connection names `trino_ref` plus a required `catalog` — the session default for unqualified table names, not a scope — and task pods reach the dock at its in-cluster address; it carries the environment's own service account, so `permission_level` does not apply to it and is refused at plan time.
* **New Data Source:** `hyperfluid_airflow` — look up an existing Airflow environment by name within a harbor.
* **New Data Source:** `hyperfluid_airflow_connection` — look up a managed connection by the `conn_id` a DAG asks for.

ENHANCEMENTS:

* `hyperfluid_service_link` accepts `Airflow` and `Pipeline` as a `consumer` kind, and refuses both as a `target` kind at plan time — matching the API, which treats each as consumer-only because both reach their targets and neither is ever reached.

BUG FIXES:

* `make fetch-spec` no longer vendors an empty spec. The console spec passed 1 MiB (~2.1 MB), above which GitHub's contents API inlines no base64 and answers `"content": ""`; `base64 -d` of nothing succeeds, so the fetch wrote a zero-byte file and exited 0. It now streams the blob with the raw media type and refuses any result that carries no `openapi` version or no paths, so a failed fetch is a red build rather than a silently emptied client.

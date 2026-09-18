## 0.1.0 (Unreleased)

FEATURES:

* **New Resource:** `hyperfluid_airflow` — a managed Apache Airflow 3 environment, with its metadata PostgreSQL cluster, DAG bucket, task-pod egress grants and `airflow.cfg` overrides.
* **New Resource:** `hyperfluid_airflow_connection` — a managed Airflow connection to a PostgreSQL cluster or a bucket; the platform mints and rotates the credential, so none ever reaches Terraform state.
* **New Data Source:** `hyperfluid_airflow` — look up an existing Airflow environment by name within a harbor.
* **New Data Source:** `hyperfluid_airflow_connection` — look up a managed connection by the `conn_id` a DAG asks for.

ENHANCEMENTS:

* `hyperfluid_service_link` accepts `Airflow` and `Pipeline` as a `consumer` kind, and refuses both as a `target` kind at plan time — matching the API, which treats each as consumer-only because both reach their targets and neither is ever reached.

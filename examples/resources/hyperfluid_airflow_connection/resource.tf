data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_bucket" "dags" {
  env  = data.hyperfluid_env.default.id
  name = "analytics-dags"
}

# What the DAGs work on: a warehouse to query and a bucket to land exports in.
# Neither is Airflow's metadata database — the platform provisions that one for
# the environment itself.
resource "hyperfluid_managed_postgresql" "warehouse" {
  env           = data.hyperfluid_env.default.id
  name          = "warehouse"
  database_name = "warehouse"
}

resource "hyperfluid_bucket" "exports" {
  env  = data.hyperfluid_env.default.id
  name = "reporting-exports"
}

resource "hyperfluid_airflow" "analytics" {
  env            = data.hyperfluid_env.default.id
  name           = "analytics"
  dag_bucket_ref = hyperfluid_bucket.dags.name
}

# A DAG asks Airflow for conn_id "warehouse" and gets a working connection: the
# platform mints the database role, grants it and writes the connection row
# itself, so no password appears in this configuration or in Terraform state.
# It also opens the network path to the cluster it just provisioned the role on,
# which is why a PostgreSQL connection needs no egress.in_cluster entry.
resource "hyperfluid_airflow_connection" "warehouse" {
  airflow = hyperfluid_airflow.analytics.id
  conn_id = "warehouse"

  managed_postgresql_ref = hyperfluid_managed_postgresql.warehouse.name

  # Pinned so the nightly load can write. Leave it out to follow the platform
  # default, which is re-resolved on every reconcile — that is a different
  # thing from pinning whatever the default happens to be today.
  permission_level = "editor"
}

# The same for object storage. Exactly one target per connection: naming both
# managed_postgresql_ref and bucket_ref, or neither, is a plan-time error.
resource "hyperfluid_airflow_connection" "exports" {
  airflow = hyperfluid_airflow.analytics.id
  conn_id = "exports"

  bucket_ref       = hyperfluid_bucket.exports.name
  permission_level = "editor"
}

# Ready means the row is in Airflow and the credential reached the target. A
# Collision means another declaration — or a row typed into the Airflow UI —
# already owns the conn_id, and this connection was not applied.
output "warehouse_connection_phase" {
  value = hyperfluid_airflow_connection.warehouse.phase
}

# What the DAG will actually be allowed to do on the bucket.
output "exports_connection_level" {
  value = hyperfluid_airflow_connection.exports.resolved_permission_level
}

data "hyperfluid_env" "default" {
  name = "default"
}

data "hyperfluid_airflow" "analytics" {
  env  = data.hyperfluid_env.default.id
  name = "analytics"
}

# Read a connection by the conn_id a DAG asks for — its declaration and its
# status, never its credential. A bucket connection's object-store session is
# minted fresh, short-lived and in-cluster only, so it is part of no read; nor is
# the client secret a Trino connection's row carries.
data "hyperfluid_airflow_connection" "warehouse" {
  airflow = data.hyperfluid_airflow.analytics.id
  conn_id = "warehouse"
}

# What access the connection actually grants: the pinned level, or the platform
# default when nothing is pinned. Read permission_level_applies first — it is
# false for a Trino connection, which carries the environment's own service
# account and is granted no authority of its own, and this field then describes
# nothing the platform granted.
output "warehouse_level" {
  value = data.hyperfluid_airflow_connection.warehouse.resolved_permission_level
}

output "warehouse_level_applies" {
  value = data.hyperfluid_airflow_connection.warehouse.permission_level_applies
}

# A Trino connection: which catalog the task pods open every session against.
data "hyperfluid_airflow_connection" "lakehouse" {
  airflow = data.hyperfluid_airflow.analytics.id
  conn_id = "lakehouse"
}

output "lakehouse_catalog" {
  value = data.hyperfluid_airflow_connection.lakehouse.catalog
}

# True only once the credential has reached the target — the difference between
# "the connection exists" and "the DAG will work".
output "warehouse_applied" {
  value = data.hyperfluid_airflow_connection.warehouse.source_applied
}

# The reason behind the phase, which is where a connection that will not apply
# says why.
output "warehouse_conditions" {
  value = data.hyperfluid_airflow_connection.warehouse.conditions
}

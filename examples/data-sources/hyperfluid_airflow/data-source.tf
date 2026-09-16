data "hyperfluid_env" "default" {
  name = "default"
}

# Reference an existing Airflow environment (created via the console, hfctl, or
# another config) to hang connections off it and to find its DAG bucket.
data "hyperfluid_airflow" "analytics" {
  env  = data.hyperfluid_env.default.id
  name = "analytics"
}

# The id a hyperfluid_airflow_connection's `airflow` takes.
output "airflow_id" {
  value = data.hyperfluid_airflow.analytics.id
}

# Upload DAGs under this bucket's "dags/" prefix; nothing outside it is picked up.
output "dag_bucket" {
  value = data.hyperfluid_airflow.analytics.dag_bucket
}

# Absent while the environment is asleep or its route is not yet serving.
output "airflow_url" {
  value = data.hyperfluid_airflow.analytics.web_url
}

# Anything listed here is an egress allow-list the last reconcile could not
# resolve in this harbor — a grant that silently is not in force.
output "unresolved_allowlists" {
  value = data.hyperfluid_airflow.analytics.unresolved_allowlists
}

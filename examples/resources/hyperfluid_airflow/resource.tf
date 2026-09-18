data "hyperfluid_env" "default" {
  name = "default"
}

# DAGs are delivered through a bucket: everything under its "dags/" prefix is
# mirrored into the dag-processor, and nothing outside that prefix is picked up.
# Naming the bucket keeps it in Terraform's hands — omit dag_bucket_ref and the
# platform provisions "<name>-airflow" instead.
resource "hyperfluid_bucket" "dags" {
  env  = data.hyperfluid_env.default.id
  name = "analytics-dags"
}

# A cache the DAGs write computed features into. Task pods cannot reach it until
# the environment is granted the path below.
resource "hyperfluid_key_value_cache" "features" {
  env  = data.hyperfluid_env.default.id
  name = "features"
}

resource "hyperfluid_airflow" "analytics" {
  env  = data.hyperfluid_env.default.id
  name = "analytics"

  dag_bucket_ref = hyperfluid_bucket.dags.name

  # There is no nano tier: 512Mi cannot hold an Airflow 3 triggerer, so the
  # catalogue starts one tier up.
  node_tier = "small"

  # Task pods get the platform baseline only (execution API, object storage,
  # Bifrost, DNS). Everything else a DAG reaches is granted here, and the whole
  # block is replaced on change rather than merged.
  egress = {
    fqdns = ["api.stripe.com", "*.googleapis.com"]

    in_cluster = [
      {
        kind = "HfKeyValueCache"
        name = hyperfluid_key_value_cache.features.slug
      },
    ]

    # A shared egress allow-list of this harbor. A name that does not resolve is
    # reported back in unresolved_allowlists rather than failing the apply.
    allowlists = ["python-packages"]
  }

  # airflow.cfg overrides, keyed section.key — the platform renders
  # "core.parallelism" as AIRFLOW__CORE__PARALLELISM. Replaced wholesale too,
  # and the settings the platform owns (executor, auth manager, execution API,
  # metadata database, remote logging, secrets backend) are refused.
  config = {
    "core.parallelism"                    = "64"
    "scheduler.min_file_process_interval" = "60"
  }

  # Ceiling on concurrent task pods in the environment's own namespace. Omit it
  # to leave the platform default in charge.
  task_quota_max_pods = 50

  description = "Nightly reporting pipelines"
  tags        = ["analytics", "terraform"]
}

output "airflow_url" {
  value = hyperfluid_airflow.analytics.web_url
}

# The bucket actually in use, whether referenced above or platform-provisioned.
# Upload DAGs under its "dags/" prefix.
output "dag_bucket" {
  value = hyperfluid_airflow.analytics.dag_bucket
}

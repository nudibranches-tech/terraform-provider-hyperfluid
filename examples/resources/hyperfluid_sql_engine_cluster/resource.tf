data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_sql_engine_cluster" "analytics" {
  env             = data.hyperfluid_env.default.id
  name            = "analytics"
  description     = "Reporting queries"
  worker_replicas = 2
  worker_memory   = "4GB"

  worker_auto_scaling = {
    enabled      = true
    min_replicas = 2
    max_replicas = 6
  }

  expose_to_internet = false
}

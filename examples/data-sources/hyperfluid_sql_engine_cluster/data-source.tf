data "hyperfluid_env" "default" {
  name = "default"
}

data "hyperfluid_sql_engine_cluster" "analytics" {
  env  = data.hyperfluid_env.default.id
  name = "analytics"
}

output "sql_engine_host" {
  value = data.hyperfluid_sql_engine_cluster.analytics.host
}

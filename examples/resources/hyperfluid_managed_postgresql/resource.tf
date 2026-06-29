data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_managed_postgresql" "db" {
  env           = data.hyperfluid_env.default.id
  name             = "appdb"
  database_name    = "appdb"
  engine           = "postgresql"
  version          = "17"
  node_tier        = "nano"
  storage_capacity = 5
  configuration    = "standalone"

  # Defaults to false (reachable only in-cluster). Set true to publish an
  # external NodePort endpoint.
  expose_to_internet = true
}

output "write_endpoint" {
  value = hyperfluid_managed_postgresql.db.write_endpoint
}

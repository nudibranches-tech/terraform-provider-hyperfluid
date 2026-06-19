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
}

output "write_endpoint" {
  value = hyperfluid_managed_postgresql.db.write_endpoint
}

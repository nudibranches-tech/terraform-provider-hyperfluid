data "hyperfluid_harbor" "default" {
  name = "default"
}

resource "hyperfluid_managed_postgresql" "db" {
  harbor           = data.hyperfluid_harbor.default.id
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

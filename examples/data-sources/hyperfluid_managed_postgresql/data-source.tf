data "hyperfluid_environment" "default" {
  name = "default"
}

# Reference an existing managed PostgreSQL cluster (created via console, CLI, or another config).
data "hyperfluid_managed_postgresql" "db" {
  env  = data.hyperfluid_environment.default.id
  name = "appdb"
}

output "write_endpoint" {
  value = data.hyperfluid_managed_postgresql.db.write_endpoint
}

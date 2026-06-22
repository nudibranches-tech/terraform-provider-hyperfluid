data "hyperfluid_env" "default" {
  name = "default"
}

# Look up the cluster by name, then a user within it.
data "hyperfluid_managed_postgresql" "db" {
  env  = data.hyperfluid_env.default.id
  name = "appdb"
}

data "hyperfluid_managed_postgresql_user" "editor" {
  managed_postgresql = data.hyperfluid_managed_postgresql.db.id
  username           = "app_editor"
}

output "user_permission" {
  value = data.hyperfluid_managed_postgresql_user.editor.permission_level
}

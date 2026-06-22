# Reference an existing PostgreSQL user by username within a cluster.
data "hyperfluid_managed_postgresql_user" "editor" {
  managed_postgresql = "00000000-0000-0000-0000-000000000000"
  username           = "app_editor"
}

output "user_permission" {
  value = data.hyperfluid_managed_postgresql_user.editor.permission_level
}

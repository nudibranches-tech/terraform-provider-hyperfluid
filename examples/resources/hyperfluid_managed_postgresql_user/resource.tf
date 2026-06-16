resource "hyperfluid_managed_postgresql_user" "editor" {
  managed_postgresql = hyperfluid_managed_postgresql.db.id
  username           = "app_editor"
  permission_level   = "editor"
}

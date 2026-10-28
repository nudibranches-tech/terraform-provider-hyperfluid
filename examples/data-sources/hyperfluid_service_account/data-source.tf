# By client_id, or by display name (which must match exactly one account).
data "hyperfluid_service_account" "backup_reader" {
  client_id = "backup-reader"
}

resource "hyperfluid_service_account_access" "backup_reader" {
  service_account_id = data.hyperfluid_service_account.backup_reader.id
  grants             = [{ permission = "bucket:read_objects", scope_path = "default/backups" }]
}

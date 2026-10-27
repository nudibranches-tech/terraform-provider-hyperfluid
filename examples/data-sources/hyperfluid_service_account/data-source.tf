# By client_id, or by display name (which must match exactly one account).
data "hyperfluid_service_account" "backup_reader" {
  client_id = "backup-reader"
}

resource "hyperfluid_group_member" "backup_reader" {
  group_id       = hyperfluid_group.auditors.id
  principal_type = "service_account"
  principal_id   = data.hyperfluid_service_account.backup_reader.id
}

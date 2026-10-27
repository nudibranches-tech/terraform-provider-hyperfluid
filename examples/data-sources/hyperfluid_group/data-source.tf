data "hyperfluid_group" "auditors" {
  name = "auditors"
}

resource "hyperfluid_grant" "auditors_read_grants" {
  principal_type = "group"
  principal_id   = data.hyperfluid_group.auditors.id
  permission     = "grant:read"
}

# The account itself is created in the console or with hfctl.
data "hyperfluid_service_account" "etl" {
  client_id = "etl"
}

resource "hyperfluid_service_account_access" "etl" {
  service_account_id = data.hyperfluid_service_account.etl.id
  roles              = [{ role_id = hyperfluid_role.auditor.id }]
  grants             = [{ permission = "bucket:read_objects", scope_path = "default/exports" }]
  attributes         = ["region::eu"]
}

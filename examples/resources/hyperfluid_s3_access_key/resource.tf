data "hyperfluid_env" "default" {
  name = "default"
}

# Service accounts are created in the console or with hfctl; Terraform
# references them. A key reaches what the account's grants allow, so give each
# consumer an account of its own (scoped keys are created in the console).
data "hyperfluid_service_account" "etl" {
  client_id = "etl"
}

resource "hyperfluid_grant" "etl_read_exports" {
  principal_type = "service_account"
  principal_id   = data.hyperfluid_service_account.etl.id
  permission     = "bucket:read_objects"
  scope_path     = "default/exports"
}

resource "hyperfluid_s3_access_key" "etl" {
  env                = data.hyperfluid_env.default.id
  service_account_id = data.hyperfluid_service_account.etl.id
  label              = "nightly export"
}

# The secret is only returned at issue, and is kept (sensitive) in state.
output "etl_secret_access_key" {
  value     = hyperfluid_s3_access_key.etl.secret_access_key
  sensitive = true
}

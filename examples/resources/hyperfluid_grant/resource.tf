# Service accounts are created in the console or with hfctl; Terraform
# references them.
data "hyperfluid_service_account" "etl" {
  client_id = "etl"
}

# Read every secret under prod/, for every member of the group.
resource "hyperfluid_grant" "data_eng_prod_secrets" {
  principal_type = "group"
  principal_id   = hyperfluid_group.data_eng.id
  permission     = "secret:read"
  scope_path     = "prod/*"
}

# A deny takes precedence over every allow.
resource "hyperfluid_grant" "etl_no_secret_updates" {
  principal_type = "service_account"
  principal_id   = data.hyperfluid_service_account.etl.id
  permission     = "secret:update"
  effect         = "deny"
}

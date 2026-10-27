# Service accounts are created in the console or with hfctl; Terraform
# references them.
data "hyperfluid_service_account" "reporting" {
  client_id = "reporting"
}

# Row-level-security attributes data access policies match against. The set is
# authoritative: attributes added elsewhere are removed on apply.
resource "hyperfluid_service_account_attributes" "reporting" {
  service_account_id = data.hyperfluid_service_account.reporting.id
  attributes         = [
    "region::occitanie",
    "department::sales",
  ]
}

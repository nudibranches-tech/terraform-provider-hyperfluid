# Service accounts are created in the console or with hfctl; Terraform
# references them.
data "hyperfluid_service_account" "etl" {
  client_id = "etl"
}

variable "alice_user_id" {
  type = string
}

resource "hyperfluid_group_member" "alice" {
  group_id       = hyperfluid_group.data_eng.id
  principal_type = "user"
  principal_id   = var.alice_user_id
}

resource "hyperfluid_group_member" "etl" {
  group_id       = hyperfluid_group.data_eng.id
  principal_type = "service_account"
  principal_id   = data.hyperfluid_service_account.etl.id
}

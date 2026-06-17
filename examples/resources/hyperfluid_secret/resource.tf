# `value` is write-only — it is sent to the API but never stored in Terraform
# state. Requires Terraform >= 1.11. To rotate, change `value` together with
# `value_wo_version`. Source the value from a variable, not a literal.
variable "db_password" {
  type      = string
  sensitive = true
}

resource "hyperfluid_secret" "db_password" {
  name             = "db-password"
  secret_type      = "plaintext"
  value            = var.db_password
  value_wo_version = "1"
}

resource "hyperfluid_secret" "config" {
  name             = "app-config"
  secret_type      = "json"
  value            = jsonencode({ feature_flags = { beta = true } })
  value_wo_version = "1"
}

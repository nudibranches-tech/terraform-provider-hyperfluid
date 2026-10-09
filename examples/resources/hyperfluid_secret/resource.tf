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

# A Git credential, read by a container app's `image_source`. The value is a JSON
# object: `provider` (github, gitlab or forgejo) and `token` are required;
# `username` and `base_url` (a self-hosted instance) are optional. A read-only
# token is enough. Create it without a scope: a Git source only accepts
# organization-wide credentials.
variable "git_token" {
  type      = string
  sensitive = true
}

resource "hyperfluid_secret" "git" {
  name             = "production/git/acme"
  secret_type      = "scm_credential"
  value            = jsonencode({ provider = "github", token = var.git_token })
  value_wo_version = "1"
}

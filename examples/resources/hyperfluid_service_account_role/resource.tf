# Service accounts are created in the console or with hfctl; Terraform
# references them.
data "hyperfluid_service_account" "ci" {
  client_id = "ci-deployer"
}

data "hyperfluid_role" "editor" {
  name = "editor"
}

# Organization-wide.
resource "hyperfluid_service_account_role" "ci_editor" {
  service_account_id = data.hyperfluid_service_account.ci.id
  role_id            = data.hyperfluid_role.editor.id
}

# Only for what lives under prod/.
resource "hyperfluid_service_account_role" "ci_deployer_prod" {
  service_account_id = data.hyperfluid_service_account.ci.id
  role_id            = hyperfluid_role.deployer.id
  scope_path         = "prod/*"
}

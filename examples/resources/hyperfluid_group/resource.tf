data "hyperfluid_user" "alice" {
  email = "alice@example.com"
}

data "hyperfluid_service_account" "etl" {
  client_id = "etl"
}

resource "hyperfluid_group" "data_eng" {
  name        = "data-engineering"
  description = "Builds and runs the pipelines"

  members = [
    { type = "user", id = data.hyperfluid_user.alice.id },
    { type = "service_account", id = data.hyperfluid_service_account.etl.id },
  ]

  roles = [{ role_id = hyperfluid_role.auditor.id, scope_path = "analytics" }]

  # The first grant is pinned to one secret, so it follows a rename.
  grants = [
    { permission = "secret:read", scope_path = hyperfluid_secret.db.name, resource_uuid = hyperfluid_secret.db.id },
    { permission = "secret:update", scope_path = "prod/*", effect = "deny" },
  ]
}

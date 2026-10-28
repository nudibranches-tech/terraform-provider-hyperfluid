data "hyperfluid_user" "alice" {
  email = "alice@example.com"
}

data "hyperfluid_role" "viewer" {
  name = "viewer"
}

resource "hyperfluid_user_access" "alice" {
  user_id = data.hyperfluid_user.alice.id
  roles = [
    { role_id = data.hyperfluid_role.viewer.id },
    { role_id = hyperfluid_role.auditor.id, scope_path = "prod/db", resource_type = "secret" },
  ]
  grants = [{ permission = "secret:read", scope_path = "prod/db" }]
}

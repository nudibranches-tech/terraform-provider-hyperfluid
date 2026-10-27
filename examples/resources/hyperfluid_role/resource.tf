# A custom role; its permissions are set by hyperfluid_role_permissions, the
# only resource that manages the bundle.
resource "hyperfluid_role" "auditor" {
  name        = "auditor"
  description = "Read-only access to IAM and secrets metadata"
  color       = "#0f766e"
}

resource "hyperfluid_role_permissions" "auditor" {
  role_id = hyperfluid_role.auditor.id
  permissions = [
    "secret:list",
    "grant:read",
    "group:read",
  ]
}

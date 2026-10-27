resource "hyperfluid_role" "auditor" {
  name = "auditor"
}

# Authoritative: the role holds exactly these permissions, and destroying the
# resource empties its bundle. System roles refuse edits.
resource "hyperfluid_role_permissions" "auditor" {
  role_id = hyperfluid_role.auditor.id
  permissions = [
    "secret:list",
    "grant:read",
    "group:read",
  ]
}

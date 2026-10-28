# The user must already be a member of the organization (invited and accepted).
data "hyperfluid_user" "alice" {
  email = "alice@example.com"
}

data "hyperfluid_role" "viewer" {
  name = "viewer"
}

resource "hyperfluid_user_access" "alice" {
  user_id = data.hyperfluid_user.alice.id
  roles   = [{ role_id = data.hyperfluid_role.viewer.id }]
}

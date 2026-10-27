variable "alice_user_id" {
  type = string
}

data "hyperfluid_role" "viewer" {
  name = "viewer"
}

# Organization-wide.
resource "hyperfluid_user_role" "alice_viewer" {
  user_id = var.alice_user_id
  role_id = data.hyperfluid_role.viewer.id
}

# Only for what lives under analytics.
resource "hyperfluid_user_role" "alice_auditor_analytics" {
  user_id    = var.alice_user_id
  role_id    = hyperfluid_role.auditor.id
  scope_path = "analytics"
}

# System roles (owner, editor, viewer) exist in every organization.
data "hyperfluid_role" "viewer" {
  name = "viewer"
}

output "viewer_permissions" {
  value = data.hyperfluid_role.viewer.permissions
}

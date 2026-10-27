# System roles (owner, editor, viewer) exist in every organization.
data "hyperfluid_role" "viewer" {
  name = "viewer"
}

resource "hyperfluid_group_role" "auditors_viewer" {
  group_id = hyperfluid_group.auditors.id
  role_id  = data.hyperfluid_role.viewer.id
}

output "viewer_permissions" {
  value = data.hyperfluid_role.viewer.permissions
}

resource "hyperfluid_role" "editor_analytics" {
  name = "analytics-editor"
}

resource "hyperfluid_group_role" "data_eng_editor" {
  group_id   = hyperfluid_group.data_eng.id
  role_id    = hyperfluid_role.editor_analytics.id
  scope_path = "analytics"
}

resource "hyperfluid_role" "auditor" {
  name        = "auditor"
  description = "Read-only access to IAM and secrets metadata"
  color       = "#0f766e"
  permissions = ["secret:list", "grant:read", "group:read"]
}

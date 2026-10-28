# Every permission key a grant or a role can hold.
data "hyperfluid_permissions" "secret" {
  resource_type = "secret"
}

output "secret_permissions" {
  value = data.hyperfluid_permissions.secret.keys
}

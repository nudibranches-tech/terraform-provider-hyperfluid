# Reference an existing secret by name (metadata only — the value is never read).
data "hyperfluid_secret" "db_password" {
  name = "db-password"
}

output "secret_path" {
  value = data.hyperfluid_secret.db_password.secret_path
}

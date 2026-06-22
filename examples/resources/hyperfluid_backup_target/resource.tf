data "hyperfluid_env" "default" {
  name = "default"
}

# Backup-target credentials are referenced by secret NAME, never inline. Create
# the secrets first (here from sensitive variables, so the values never land in
# the Terraform config or state) and reference them by name below.
variable "backup_access_key_id" {
  type      = string
  sensitive = true
}

variable "backup_secret_access_key" {
  type      = string
  sensitive = true
}

resource "hyperfluid_secret" "backup_access_key" {
  name             = "backup-access-key"
  secret_type      = "plaintext"
  value            = var.backup_access_key_id
  value_wo_version = "1"
}

resource "hyperfluid_secret" "backup_secret_key" {
  name             = "backup-secret-key"
  secret_type      = "plaintext"
  value            = var.backup_secret_access_key
  value_wo_version = "1"
}

# The target only becomes Ready after the operator probes the endpoint
# (HeadBucket + write/read/list/delete), so endpoint_url, destination_path, and
# the credentials must point at a bucket that already exists and is writable.
# Note: insecure = true is not supported with an https:// endpoint — use http://
# or a trusted TLS cert.
resource "hyperfluid_backup_target" "offsite" {
  env          = data.hyperfluid_env.default.id
  name         = "offsite"
  endpoint_url = "https://s3.eu-west-3.amazonaws.com"
  # Bucket + an arbitrary prefix to store backups under (NOT a database name);
  # each cluster's backups are laid out beneath it as <prefix>/<server>/...
  destination_path              = "s3://my-backups/hyperfluid/"
  access_key_secret_name        = hyperfluid_secret.backup_access_key.name
  secret_access_key_secret_name = hyperfluid_secret.backup_secret_key.name
}

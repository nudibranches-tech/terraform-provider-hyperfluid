data "hyperfluid_env" "default" {
  name = "default"
}

# Credentials are referenced by secret name, never inline.
resource "hyperfluid_backup_target" "offsite" {
  env                        = data.hyperfluid_env.default.id
  name                          = "offsite"
  endpoint_url                  = "https://s3.eu-west-3.amazonaws.com"
  destination_path              = "s3://my-backups/postgres/"
  access_key_secret_name        = "aws-backup-access-key"
  secret_access_key_secret_name = "aws-backup-secret-key"
}

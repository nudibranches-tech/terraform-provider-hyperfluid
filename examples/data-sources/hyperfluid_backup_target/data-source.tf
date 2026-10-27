data "hyperfluid_environment" "default" {
  name = "default"
}

data "hyperfluid_backup_target" "offsite" {
  env  = data.hyperfluid_environment.default.id
  name = "offsite"
}

output "backup_endpoint" {
  value = data.hyperfluid_backup_target.offsite.endpoint_url
}

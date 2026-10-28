data "hyperfluid_environment" "default" {
  name = "default"
}

output "env_id" {
  value = data.hyperfluid_environment.default.id
}

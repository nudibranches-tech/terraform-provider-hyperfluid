data "hyperfluid_env" "default" {
  name = "default"
}

output "env_id" {
  value = data.hyperfluid_env.default.id
}

data "hyperfluid_environment" "default" {
  name = "default"
}

# Reference an existing container app to read its endpoint and status.
data "hyperfluid_container_app" "web" {
  env  = data.hyperfluid_environment.default.id
  slug = "web"
}

output "app_endpoint" {
  value = data.hyperfluid_container_app.web.endpoint
}

# The app's plain environment variables, as a name => value map.
output "app_env" {
  value = { for e in data.hyperfluid_container_app.web.env_vars : e.name => e.value if e.secret == null }
}

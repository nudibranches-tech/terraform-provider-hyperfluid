data "hyperfluid_env" "default" {
  name = "default"
}

# Reference an existing container app to read its endpoint and status.
data "hyperfluid_container_app" "web" {
  env  = data.hyperfluid_env.default.id
  name = "web"
}

output "app_endpoint" {
  value = data.hyperfluid_container_app.web.endpoint
}

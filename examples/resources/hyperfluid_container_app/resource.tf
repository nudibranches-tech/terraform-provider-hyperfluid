data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "web" {
  env           = data.hyperfluid_env.default.id
  name             = "web"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  port             = 8080
  replicas         = 1
  resource_tier    = "nano"

  # Defaults to false (reachable only in-cluster). Set true to create
  # internet-facing routes.
  expose_to_internet = true
}

output "endpoint" {
  value = hyperfluid_container_app.web.endpoint
}

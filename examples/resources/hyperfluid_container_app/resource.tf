data "hyperfluid_harbor" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "web" {
  harbor           = data.hyperfluid_harbor.default.id
  name             = "web"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  port             = 8080
  replicas         = 1
  resource_tier    = "nano"
}

output "endpoint" {
  value = hyperfluid_container_app.web.endpoint
}

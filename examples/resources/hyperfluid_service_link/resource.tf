data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "web" {
  env              = data.hyperfluid_env.default.id
  name             = "web"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
  ]
  resource_tier = "nano"
}

resource "hyperfluid_managed_postgresql" "main" {
  env           = data.hyperfluid_env.default.id
  name          = "main"
  database_name = "app"
}

# A link is only enforced in an environment whose network isolation is strict.
# Under the default lenient mode every service in the environment can already
# reach every other, so a link records intent rather than changing reachability.
#
# Postgres publishes one known port, so the platform opens it — no target_ports.
resource "hyperfluid_service_link" "web_to_db" {
  env = data.hyperfluid_env.default.id

  consumer = {
    kind = "ContainerApp"
    name = hyperfluid_container_app.web.slug
  }

  target = {
    kind = "ManagedPostgreSQL"
    name = hyperfluid_managed_postgresql.main.slug
  }
}

# A multi-port app: only the primary port gets a public route, the rest are
# reachable in-cluster — which is exactly what a link opens.
resource "hyperfluid_container_app" "api" {
  env              = data.hyperfluid_env.default.id
  name             = "api"
  image_repository = "nginxinc/nginx-unprivileged"
  image_tag        = "alpine"
  resource_tier    = "nano"

  ports = [
    {
      name     = "http"
      port     = 8080
      protocol = "HTTP"
      primary  = true
    },
    {
      name     = "metrics"
      port     = 9090
      protocol = "TCP"
    },
  ]
}

# A container app is the one kind publishing several ports, so which of them to
# open can be chosen. Each pair must already be published by the target app.
resource "hyperfluid_service_link" "web_to_api" {
  env = data.hyperfluid_env.default.id

  consumer = {
    kind = "ContainerApp"
    name = hyperfluid_container_app.web.slug
  }

  target = {
    kind = "ContainerApp"
    name = hyperfluid_container_app.api.slug
  }

  target_ports = [
    { port = 9090 }, # the metrics port; protocol defaults to TCP
  ]
}

# The target's own ports attribute can be assigned straight through — its extra
# name/primary attributes are ignored. Note that this ties the link to the app's
# plan: while the app has a pending change its ports are unknown, and since every
# attribute of a link is immutable, the link is then planned for replacement.
resource "hyperfluid_service_link" "web_to_api_all_ports" {
  env = data.hyperfluid_env.default.id

  consumer     = { kind = "ContainerApp", name = hyperfluid_container_app.web.slug }
  target       = { kind = "ContainerApp", name = hyperfluid_container_app.api.slug }
  target_ports = hyperfluid_container_app.api.ports
}

# The name is derived by the platform from the linked pair.
output "link_name" {
  value = hyperfluid_service_link.web_to_db.name
}

# The ports actually opened, including any the platform picked itself.
output "open_ports" {
  value = hyperfluid_service_link.web_to_db.ports
}

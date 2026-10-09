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
  replicas      = 1
  resource_tier = "nano"

  # Defaults to false (reachable only in-cluster). Set true to create
  # internet-facing routes.
  expose_to_internet = true
}

output "endpoint" {
  value = hyperfluid_container_app.web.endpoint
}

# Deploy what a Git repository declares instead of a literal image. The platform
# polls `hyperfluid.toml` in the repository and rolls the app out when this app's
# entry changes. Push access to the tracked branch (or to a matching tag) is
# deploy access: protect it.
#
# `image_source` conflicts with `image_repository` / `image_tag`. The image that
# runs is reported in `resolved_image`, which Terraform never plans a change for,
# so a release made through Git is not drift.
variable "git_token" {
  type      = string
  sensitive = true
}

# Every Git source reads with a credential, public repositories included, so its
# checks count against the token's own rate limit rather than the anonymous one
# every app of the cluster shares. A read-only token is enough. It must be an
# organization-wide secret, which is what `hyperfluid_secret` creates.
resource "hyperfluid_secret" "git" {
  name             = "production/git/acme"
  secret_type      = "scm_credential"
  value            = jsonencode({ provider = "github", token = var.git_token })
  value_wo_version = "1"
}

resource "hyperfluid_container_app" "orders" {
  env  = data.hyperfluid_env.default.id
  name = "orders-api"
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
  ]
  resource_tier = "nano"

  image_source {
    git {
      provider   = "github"
      repository = "acme/orders-api"
      credential = hyperfluid_secret.git.name

      # Optional, and left to the platform when omitted: the default branch, the
      # `hyperfluid.toml` file, an entry named after the app, a check every 5m.
      # branch      = "main"       # or: tag_pattern = "v?(?<version>\\d+\\.\\d+\\.\\d+)"
      # path        = "deploy/orders-api/hyperfluid.toml"
      # container   = "orders-api"
      # interval    = "15m"
      # alert_on_sync_failure = false
    }
  }
}

output "orders_image" {
  value = hyperfluid_container_app.orders.resolved_image
}

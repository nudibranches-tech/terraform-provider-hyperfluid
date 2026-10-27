data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_container_app" "web" {
  env              = data.hyperfluid_env.default.id
  slug             = "web"
  display_name     = "Web storefront"
  description      = "Public storefront."
  tags             = ["storefront", "public"]
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

# Environment variables, secrets and init containers. `env_vars` is ordered: a
# value can reference a variable listed before it as `$(NAME)`. Its values are
# plain text in the app's spec and in Terraform state, so a credential goes in
# an organization-wide `hyperfluid_secret` read through `secret_refs`.
#
# Each of these lists, once applied, is managed by Terraform: removing it from
# the configuration clears it on the app. Left out from the start, it is not
# managed, so what the console or an app template set survives an apply.
variable "db_password" {
  type      = string
  sensitive = true
}

resource "hyperfluid_secret" "db" {
  name             = "production/orders/db-password"
  secret_type      = "plaintext"
  value            = var.db_password
  value_wo_version = "1"
}

resource "hyperfluid_container_app" "api" {
  env              = data.hyperfluid_env.default.id
  slug             = "orders"
  image_repository = "ghcr.io/acme/orders"
  image_tag        = "2.3.0"
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
  ]
  resource_tier = "small"

  env_vars = [
    { name = "DB_HOST", value = "orders-db" },
    { name = "DB_URL", value = "postgres://orders@$(DB_HOST):5432/orders" },
  ]
  secret_refs = [
    { env_var_name = "DB_PASSWORD", secret_name = hyperfluid_secret.db.name },
  ]

  # Run to completion, in order, before the app starts. With no image of its
  # own an init container runs the app's image, and follows it on every
  # upgrade. Each inherits the app's environment and volumes unless told not to.
  init_containers = [
    { name = "migrate", args = ["migrate", "up"] },
    {
      name             = "wait-for-db"
      image_repository = "busybox"
      image_tag        = "1.37"
      args             = ["sh", "-c", "until nc -z orders-db 5432; do sleep 2; done"]
      inherit_env      = false
    },
  ]

  # Overriding the entrypoint or arguments, here or on an init container, needs
  # the organization's container command override feature; without it the
  # platform refuses the apply. Leave command and args out to run the image's own.
  # command = ["/app/orders"]
  # args    = ["serve", "--port", "8080"]
}

# Sizing, scaling, files, domains and metrics. Like the lists above, `file_mounts`,
# `custom_domains`, `autoscaling` and `metrics` are managed once applied and left
# to the app while they never were.
resource "hyperfluid_container_app" "storefront" {
  env              = data.hyperfluid_env.default.id
  slug             = "storefront"
  image_repository = "ghcr.io/acme/storefront"
  image_tag        = "4.1.0"
  ports = [
    { name = "http", port = 8080, protocol = "HTTP", primary = true },
    { name = "metrics", port = 9090, protocol = "TCP" },
  ]
  expose_to_internet = true

  # A size between two tiers, instead of resource_tier.
  custom_resources = { cpu_millis = 750, memory_mib = 768 }

  # Replaces `replicas`. Scaling to zero wakes the app through its public route.
  autoscaling = {
    min_replicas           = 1
    max_replicas           = 4
    target_cpu_utilization = 70
  }

  # `port` names an entry of `ports`.
  metrics = { port = "metrics", path = "/metrics" }

  # The organization must have verified acme.com (or shop.acme.com) first.
  # `custom_domain_statuses` reports how far each domain got.
  custom_domains = [
    { fqdn = "shop.acme.com", tls_mode = "acme" },
  ]

  # Inline content is plain text in state, 8 KiB per file and 32 KiB in all;
  # anything sensitive comes from an organization-wide secret.
  file_mounts = [
    { mount_path = "/etc/storefront/config.yaml", content = file("${path.module}/storefront.yaml") },
    { mount_path = "/etc/storefront/license", secret_name = "production/storefront/license", mode = "0400" },
  ]
}

output "storefront_domains" {
  value = hyperfluid_container_app.storefront.custom_domain_statuses
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
  slug = "orders-api"
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
      # sync_policy = "manual"     # report changes and wait for Sync (default: auto)
      # alert_on_sync_failure = false
    }
  }
}

output "orders_image" {
  value = hyperfluid_container_app.orders.resolved_image
}

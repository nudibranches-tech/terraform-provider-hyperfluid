data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_rabbitmq" "orders" {
  env      = data.hyperfluid_env.default.id
  name     = "orders"
  replicas = 3      # 1, 3 or 5; can only grow
  storage  = "20Gi" # per node; can only grow
  plugins  = ["stream", "shovel"]

  resources = {
    requests = {
      cpu    = "500m"
      memory = "1Gi"
    }
    limits = {
      memory = "2Gi"
    }
  }
}

# Credentials live in the secret manager; the provider only exposes their
# names. Reference them from an app with `secret:<name>`.
output "amqp_uri_secret" {
  value = hyperfluid_rabbitmq.orders.amqp_uri_secret_name
}

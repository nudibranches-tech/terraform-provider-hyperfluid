data "hyperfluid_env" "default" {
  name = "default"
}

data "hyperfluid_rabbitmq" "orders" {
  env  = data.hyperfluid_env.default.id
  name = "orders"
}

output "broker_endpoint" {
  value = "${data.hyperfluid_rabbitmq.orders.host}:${data.hyperfluid_rabbitmq.orders.amqp_port}"
}

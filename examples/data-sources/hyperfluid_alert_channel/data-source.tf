data "hyperfluid_alert_channel" "ops" {
  name = "ops-mail"
}

output "ops_channel_id" {
  value = data.hyperfluid_alert_channel.ops.id
}

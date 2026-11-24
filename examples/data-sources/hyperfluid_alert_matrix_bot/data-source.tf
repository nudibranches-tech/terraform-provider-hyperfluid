data "hyperfluid_alert_matrix_bot" "tchap" {
  name = "tchap-alerts"
}

output "tchap_bot_phase" {
  value = data.hyperfluid_alert_matrix_bot.tchap.phase
}

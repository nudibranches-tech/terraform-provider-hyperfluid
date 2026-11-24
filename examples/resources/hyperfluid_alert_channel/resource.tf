resource "hyperfluid_alert_channel" "ops_mail" {
  name       = "ops-mail"
  kind       = "email"
  recipients = ["ops@acme.io", "sre@acme.io"]
}

# `url` is write-only (Terraform >= 1.11): the platform only returns it masked.
# To rotate it, change `url` together with `url_wo_version`.
variable "slack_webhook_url" {
  type      = string
  sensitive = true
}

resource "hyperfluid_alert_channel" "ops_slack" {
  name           = "ops-slack"
  kind           = "slack_webhook"
  url            = var.slack_webhook_url
  url_wo_version = "1"
}

# A Microsoft Teams channel takes the URL of a Teams Workflows webhook
# ("Post to a channel when a webhook request is received").
variable "teams_webhook_url" {
  type      = string
  sensitive = true
}

resource "hyperfluid_alert_channel" "ops_teams" {
  name           = "ops-teams"
  kind           = "teams_webhook"
  url            = var.teams_webhook_url
  url_wo_version = "1"
}

# A Matrix or Tchap room, posted to by a hyperfluid_alert_matrix_bot. `room`
# takes a room id (`!…`) or an alias (`#…:server`); an alias is resolved by the
# platform, kept as configured here, and the resolved id is in `matrix.room_id`.
resource "hyperfluid_alert_channel" "ops_tchap" {
  name = "ops-tchap"
  kind = "matrix"
  matrix = {
    bot_id = hyperfluid_alert_matrix_bot.tchap.id
    room   = "#ops-alerts:agent.dinum.tchap.gouv.fr"
  }
}

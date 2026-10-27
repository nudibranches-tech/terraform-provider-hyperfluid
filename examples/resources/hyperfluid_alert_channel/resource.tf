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

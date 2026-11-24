# The bot account's password and recovery key live in plaintext secrets; the
# bot only references them by id.
variable "tchap_bot_password" {
  type      = string
  sensitive = true
}

variable "tchap_bot_recovery_key" {
  type      = string
  sensitive = true
}

resource "hyperfluid_secret" "tchap_bot_password" {
  name             = "tchap-bot-password"
  secret_type      = "plaintext"
  value            = var.tchap_bot_password
  value_wo_version = "1"
}

resource "hyperfluid_secret" "tchap_bot_recovery_key" {
  name             = "tchap-bot-recovery-key"
  secret_type      = "plaintext"
  value            = var.tchap_bot_recovery_key
  value_wo_version = "1"
}

resource "hyperfluid_alert_matrix_bot" "tchap" {
  name                   = "tchap-alerts"
  preset                 = "tchap"
  user_id                = "@bot-alerts:agent.dinum.tchap.gouv.fr"
  password_secret_id     = hyperfluid_secret.tchap_bot_password.id
  recovery_key_secret_id = hyperfluid_secret.tchap_bot_recovery_key.id
}

# Any other homeserver takes the `generic` preset and its https homeserver:
#
#   preset         = "generic"
#   homeserver_url = "https://matrix.example.org"
#   user_id        = "@alerts:example.org"
#
# Set `allow_unencrypted_rooms = true` to let the bot post into a room that is
# not end-to-end encrypted. When the bot's session ends (`relogin_required`),
# make it log in again from the console or with
# `hfctl alerting matrix-bots relogin <name>`.

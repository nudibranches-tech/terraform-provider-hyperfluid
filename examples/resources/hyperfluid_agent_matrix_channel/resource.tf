# The bot account's password and recovery key, stored once as plaintext
# secrets (for instance with `hfctl secrets create plaintext`). Terraform only
# references them by id.
data "hyperfluid_secret" "bot_password" {
  name = "tchap-bot-password"
}

data "hyperfluid_secret" "bot_recovery_key" {
  name = "tchap-bot-recovery-key"
}

resource "hyperfluid_agent_matrix_channel" "this" {
  agent_id = hyperfluid_agent.support.id
  preset   = "tchap"
  user_id  = "@bot-support:example.org"

  password_secret_id     = data.hyperfluid_secret.bot_password.id
  recovery_key_secret_id = data.hyperfluid_secret.bot_recovery_key.id

  allowed_users   = ["@*:example.org"]
  direct_messages = true

  # Change to a new value when `relogin_required` is true.
  relogin_trigger = "1"
}

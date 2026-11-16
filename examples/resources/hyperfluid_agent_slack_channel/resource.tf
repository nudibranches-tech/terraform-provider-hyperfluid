data "hyperfluid_secret" "slack_bot_token" {
  name = "slack-bot-token"
}

data "hyperfluid_secret" "slack_app_token" {
  name = "slack-app-token"
}

resource "hyperfluid_agent_slack_channel" "this" {
  agent_id            = hyperfluid_agent.support.id
  bot_token_secret_id = data.hyperfluid_secret.slack_bot_token.id
  app_token_secret_id = data.hyperfluid_secret.slack_app_token.id
  team_id             = "T0123456789"
  allowed_channels    = ["C0123456789"]
}

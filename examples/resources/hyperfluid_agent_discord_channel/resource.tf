data "hyperfluid_secret" "discord_bot_token" {
  name = "discord-bot-token"
}

resource "hyperfluid_agent_discord_channel" "this" {
  agent_id            = hyperfluid_agent.support.id
  bot_token_secret_id = data.hyperfluid_secret.discord_bot_token.id
  allowed_guilds      = ["123456789012345678"]
}

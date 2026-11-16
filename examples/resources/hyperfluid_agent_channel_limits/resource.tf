# Shared by the web widget and the chat channels, counted apart for each.
resource "hyperfluid_agent_channel_limits" "this" {
  agent_id           = hyperfluid_agent.support.id
  questions_per_hour = 200
  questions_per_day  = 1000
  per_sender_burst   = 6
  max_concurrent     = 4
}

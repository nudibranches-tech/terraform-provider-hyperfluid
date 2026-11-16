# Paste the manifest in Slack's "Create an app from a manifest", then store the
# app's bot token and app-level token as plaintext secrets.
data "hyperfluid_agent_slack_manifest" "support" {
  agent_id = hyperfluid_agent.support.id
}

output "slack_manifest" {
  value = data.hyperfluid_agent_slack_manifest.support.manifest
}

data "hyperfluid_environment" "default" {
  name = "default"
}

# A widget answers anonymous visitors, so the platform wants a viewer-only
# service account and an explicit tool list without general `hfctl`.
data "hyperfluid_service_account" "public_reader" {
  name = "public-data-reader"
}

resource "hyperfluid_agent" "assistant" {
  env                = data.hyperfluid_environment.default.id
  name               = "open-data-assistant"
  system_prompt      = "You answer questions about our published datasets."
  tools              = ["hfctl:query"]
  service_account_id = data.hyperfluid_service_account.public_reader.id

  inference = {
    provider = "sapience"
    model    = "sapience:shared/qwen3-32b"
  }
}

resource "hyperfluid_agent_widget" "site" {
  agent_id        = hyperfluid_agent.assistant.id
  allowed_origins = ["https://www.example.org"]
  privacy_url     = "https://www.example.org/privacy"

  title       = "Data assistant"
  greeting    = "Ask me about our open data."
  colour      = "#0b6e69"
  lang        = "en"
  suggestions = ["Which datasets do you publish?", "How often is the data refreshed?"]

  # Change this value to rotate the widget id: every copy of the old tag stops
  # working, and embed_snippet changes with it.
  rotation_trigger = "2026-11"
}

# Paste this in the site's template. It loads the floating script, which
# follows the console's fixes.
output "widget_snippet" {
  value = hyperfluid_agent_widget.site.embed_snippet
}

# The pinned form, for a security review that requires Subresource Integrity.
# Its path changes on every console upgrade: copy it again then.
output "widget_snippet_sri" {
  value = format(
    "<script defer src=\"%s%s\" integrity=\"%s\" crossorigin=\"anonymous\" data-agent=\"%s\"></script>",
    "https://console.example.org",
    hyperfluid_agent_widget.site.script_pinned_path,
    hyperfluid_agent_widget.site.script_integrity,
    hyperfluid_agent_widget.site.widget_id,
  )
}

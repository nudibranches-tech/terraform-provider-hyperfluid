# One public agent, declared in Git: the agent, its web widget on the public
# site, and (once the provider supports chat channels) a Tchap room for staff.
#
# Every name below is a placeholder. Credentials come from `hfctl tf auth` or
# HYPERFLUID_CREDENTIALS; no secret value appears in this configuration or in
# the state.

terraform {
  required_providers {
    hyperfluid = {
      source = "nudibranches-tech/hyperfluid"
    }
  }
}

provider "hyperfluid" {
  endpoint = "https://console.example.org"
}

data "hyperfluid_environment" "this" {
  name = "public-data"
}

# Viewer-only: everything this account can read, anonymous visitors can get
# answers from.
data "hyperfluid_service_account" "public_reader" {
  name = "public-catalogue-reader"
}

resource "hyperfluid_agent" "catalogue" {
  env                = data.hyperfluid_environment.this.id
  name               = "catalogue-assistant"
  description        = "Answers questions about the published catalogue."
  system_prompt      = "You help visitors find and understand the datasets we publish. Answer from the catalogue only."
  tools              = ["hfctl:query"]
  service_account_id = data.hyperfluid_service_account.public_reader.id

  inference = {
    provider = "sapience"
    model    = "sapience:shared/qwen3-32b"
  }

  data_sources = [
    {
      data_dock_id = "00000000-0000-0000-0000-000000000000"
      slug         = "catalogue"
      tables       = ["catalogue.public.datasets"]
    },
  ]
}

# The chat bubble on the public site.
resource "hyperfluid_agent_widget" "site" {
  agent_id        = hyperfluid_agent.catalogue.id
  allowed_origins = ["https://www.example.org", "https://data.example.org"]
  privacy_url     = "https://www.example.org/privacy"

  title       = "Catalogue assistant"
  greeting    = "Ask me which datasets we publish and how to use them."
  colour      = "#0b6e69"
  lang        = "fr"
  suggestions = ["Which datasets cover water quality?", "How do I cite a dataset?"]

  session_retention_hours = 24
  max_question_length     = 800

  # Bump to revoke every copy of the tag (for instance after it was copied to a
  # site that should not have it); the site template picks up the new snippet.
  rotation_trigger = "1"
}

# Feed the site's template, for instance as a CI variable of the site repository.
output "widget_snippet" {
  value = hyperfluid_agent_widget.site.embed_snippet
}

output "widget_warnings" {
  value = hyperfluid_agent_widget.site.warnings
}

# Tchap (Matrix) room for staff: not in this provider release yet. The console
# API for chat channels is being built; the intended shape, for review, is:
#
# resource "hyperfluid_agent_matrix_channel" "staff" {
#   agent_id               = hyperfluid_agent.catalogue.id
#   preset                 = "tchap"
#   homeserver_url         = "https://matrix.example.org"
#   user_id                = "@bot-catalogue:example.org"
#   password_secret_id     = "00000000-0000-0000-0000-000000000000" # a hyperfluid_secret, never the value
#   recovery_key_secret_id = "00000000-0000-0000-0000-000000000000"
#   allowed_users          = ["@*:example.org"]
#   relogin_trigger        = "1"
# }

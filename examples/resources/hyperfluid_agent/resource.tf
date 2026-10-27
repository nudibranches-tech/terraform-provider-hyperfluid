data "hyperfluid_environment" "default" {
  name = "default"
}

data "hyperfluid_service_account" "reader" {
  name = "agent-reader"
}

# A routing agent dispatches each question to the capability that answers it.
resource "hyperfluid_agent" "support" {
  env           = data.hyperfluid_environment.default.id
  name          = "support"
  speciality    = "routing"
  system_prompt = "You help our analysts find and read their data."

  inference = {
    provider = "sapience"
    model    = "sapience:shared/qwen3-32b"
  }
}

# A capability: the platform names it and fills its system prompt and tools.
resource "hyperfluid_agent" "sql" {
  env                = data.hyperfluid_environment.default.id
  speciality         = "data-sql"
  subagent_of        = hyperfluid_agent.support.slug
  mission            = "Answer from the sales tables only."
  service_account_id = data.hyperfluid_service_account.reader.id

  inference = {
    provider = "sapience"
    model    = "sapience:shared/qwen3-32b"
  }

  data_sources = [
    {
      data_dock_id = "00000000-0000-0000-0000-000000000000"
      slug         = "sales"
      tables       = ["sales.public.orders"]
    },
  ]
}

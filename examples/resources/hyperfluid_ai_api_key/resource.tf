resource "hyperfluid_ai_api_key" "gateway" {
  name = "ci-inference"

  # Scopes are model-scoped: "model:*" for every model, or
  # "model:<model_id>" to restrict the key to one model.
  scopes = ["model:*"]

  # Optional lifetime; omit for a non-expiring key.
  expires_in_days = 90
}

# The secret is only available here (and in state) — capture it on create.
output "api_key" {
  value     = hyperfluid_ai_api_key.gateway.key
  sensitive = true
}

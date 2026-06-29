# Look up an existing API key's metadata by name (the secret is never returned).
data "hyperfluid_ai_api_key" "gateway" {
  name = "ci-inference"
}

output "key_prefix" {
  value = data.hyperfluid_ai_api_key.gateway.key_prefix
}

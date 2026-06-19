data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_key_value_cache" "sessions" {
  env           = data.hyperfluid_env.default.id
  name             = "sessions"
  maxmemory        = "256mb"
  maxmemory_policy = "allkeys-lru"
}

# Connection credentials are exposed as a secret reference, not plaintext.
output "credentials_secret" {
  value = hyperfluid_key_value_cache.sessions.credentials_secret_name
}

data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_key_value_cache" "sessions" {
  env              = data.hyperfluid_env.default.id
  name             = "sessions"
  maxmemory        = "256mb"
  maxmemory_policy = "allkeys-lru"

  # Container requests and limits. Leave limits.cpu out: a CPU limit throttles
  # the cache even on an idle node. Without maxmemory, Valkey's is derived as
  # three quarters of limits.memory.
  resources = {
    requests = { cpu = "100m", memory = "256Mi" }
    limits   = { memory = "384Mi" }
  }
}

# Connection credentials are exposed as a secret reference, not plaintext.
output "credentials_secret" {
  value = hyperfluid_key_value_cache.sessions.credentials_secret_name
}

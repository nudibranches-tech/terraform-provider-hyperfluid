data "hyperfluid_env" "default" {
  name = "default"
}

data "hyperfluid_key_value_cache" "sessions" {
  env  = data.hyperfluid_env.default.id
  name = "sessions"
}

output "cache_host" {
  value = data.hyperfluid_key_value_cache.sessions.host
}

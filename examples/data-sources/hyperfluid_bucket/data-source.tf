data "hyperfluid_env" "default" {
  name = "default"
}

# Reference an existing bucket (created via the console, CLI, or another config).
data "hyperfluid_bucket" "lake" {
  env  = data.hyperfluid_env.default.id
  name = "data-lake"
}

output "bucket_ready" {
  value = data.hyperfluid_bucket.lake.ready
}

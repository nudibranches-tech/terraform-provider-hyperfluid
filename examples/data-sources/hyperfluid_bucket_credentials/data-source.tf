data "hyperfluid_env" "default" {
  name = "default"
}

# Mint the derived S3 credentials for an existing bucket. The secret_key is
# stored in Terraform state, so use an encrypted remote backend.
data "hyperfluid_bucket_credentials" "lake" {
  env         = data.hyperfluid_env.default.id
  bucket_name = "data-lake"
}

output "bucket_endpoint" {
  value = data.hyperfluid_bucket_credentials.lake.endpoint
}

output "bucket_access_key" {
  value = data.hyperfluid_bucket_credentials.lake.access_key
}

output "bucket_secret_key" {
  value     = data.hyperfluid_bucket_credentials.lake.secret_key
  sensitive = true
}

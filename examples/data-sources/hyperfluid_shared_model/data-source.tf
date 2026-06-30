# Look up a platform shared model (one the organization can consume).
data "hyperfluid_shared_model" "embeddings" {
  name = "bge-large"
}

output "shared_model_endpoint" {
  value = data.hyperfluid_shared_model.embeddings.endpoint
}

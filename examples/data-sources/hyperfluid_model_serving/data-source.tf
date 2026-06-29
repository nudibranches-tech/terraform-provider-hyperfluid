# Reference an existing model serving to read its OpenAI-compatible endpoint.
data "hyperfluid_model_serving" "embeddings" {
  name = "bge-large"
}

output "endpoint" {
  value = data.hyperfluid_model_serving.embeddings.endpoint
}

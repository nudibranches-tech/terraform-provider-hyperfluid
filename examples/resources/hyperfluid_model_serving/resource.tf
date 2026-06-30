resource "hyperfluid_model_serving" "embeddings" {
  display_name = "bge-large"
  model_id     = "BAAI/bge-large-en-v1.5"
  model_type   = "embedding"
  runtime      = "vllm"
  gpu          = 1

  # The only attribute that updates in place — everything else forces a new
  # serving. Omit to let the platform pick a default.
  replicas = 1
}

output "endpoint" {
  value = hyperfluid_model_serving.embeddings.endpoint
}

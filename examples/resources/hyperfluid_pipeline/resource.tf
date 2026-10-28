variable "documents_container_id" {
  description = "Id of the Iceberg data container that receives the extracted documents."
  type        = string
}

# Converts the documents dropped in a bucket to Markdown, into an Iceberg table.
resource "hyperfluid_pipeline" "invoices" {
  env      = data.hyperfluid_env.default.id
  name     = "invoices"
  type     = "trino"
  schedule = "*/30 * * * *"

  spec = jsonencode({
    pipeline_type = "markitdown"
    source = {
      type        = "harbor_bucket"
      bucket_name = hyperfluid_bucket.inbox.name
    }
    metadata_storage = {
      data_container_id = var.documents_container_id
      schema_name       = "documents"
      table_name        = "invoices"
    }
  })
}

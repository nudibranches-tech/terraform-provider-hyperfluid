variable "rag_data_container_id" {
  description = "Id of the Iceberg data container that receives the table."
  type        = string
}

# Reloads the Cat OPIDoR directory into directory.cat_opidor every day at
# 06:00 UTC (full refresh), with the `cat_opidor` opendata connector.
resource "hyperfluid_open_data_pipeline" "cat_opidor" {
  env            = data.hyperfluid_environment.default.id
  name           = "opendata-cat-opidor"
  connector_slug = "cat_opidor"
  params = jsonencode({
    scope           = "services_structures_ateliers"
    include_stopped = false
  })
  data_container_id = var.rag_data_container_id
  schema_name       = "directory"
  table_name        = "cat_opidor"
  schedule          = "0 6 * * *"
}

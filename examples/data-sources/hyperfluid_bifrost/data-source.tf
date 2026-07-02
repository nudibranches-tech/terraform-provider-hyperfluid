# Bifrost is the cluster's query layer — a per-deployment singleton, so this
# data source takes no arguments.
data "hyperfluid_bifrost" "this" {}

output "bifrost_rest_url" {
  value = data.hyperfluid_bifrost.this.rest_url
}

output "bifrost_pgwire_endpoint" {
  value = data.hyperfluid_bifrost.this.pgwire_endpoint
}

output "bifrost_graphql_enabled" {
  value = data.hyperfluid_bifrost.this.features.graphql_enabled
}

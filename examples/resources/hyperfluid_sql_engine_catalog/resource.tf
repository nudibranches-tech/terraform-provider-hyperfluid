# An Iceberg warehouse in a bucket of the environment.
resource "hyperfluid_sql_engine_catalog" "lake" {
  cluster_id  = hyperfluid_sql_engine_cluster.analytics.id
  name        = "lake"
  kind        = "iceberg"
  bucket_name = hyperfluid_bucket.warehouse.name
}

# A managed database, read through one of its viewer users.
resource "hyperfluid_sql_engine_catalog" "orders" {
  cluster_id                 = hyperfluid_sql_engine_cluster.analytics.id
  name                       = "orders"
  kind                       = "postgresql"
  managed_postgresql_user_id = hyperfluid_managed_postgresql_user.reader.id
}

# An external database, reached with a connection bundle stored as a secret.
resource "hyperfluid_sql_engine_catalog" "crm" {
  cluster_id        = hyperfluid_sql_engine_cluster.analytics.id
  name              = "crm"
  kind              = "mysql"
  connection_secret = hyperfluid_secret.crm_connection.name
}

data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_bucket" "lake" {
  env  = data.hyperfluid_env.default.id
  name = "data-lake"
}

# Pin a bucket to an explicit storage zone (here the primary "default" zone).
# The zone must be enabled for the organization; omit storage_zone_id to use
# the primary zone implicitly.
data "hyperfluid_storage_zone" "default" {
  zone_id = "default"
}

resource "hyperfluid_bucket" "lake_pinned" {
  env             = data.hyperfluid_env.default.id
  name            = "data-lake-pinned"
  storage_zone_id = data.hyperfluid_storage_zone.default.zone_id
}

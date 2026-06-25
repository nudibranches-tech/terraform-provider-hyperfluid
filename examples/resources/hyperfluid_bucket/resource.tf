data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_bucket" "lake" {
  env  = data.hyperfluid_env.default.id
  name = "data-lake"
}

# Place a bucket in a specific (non-primary) storage zone. The zone must be
# enabled for the organization. Omit storage_zone_id to use the primary zone.
data "hyperfluid_storage_zone" "eu" {
  zone_id = "eu-west"
}

resource "hyperfluid_bucket" "lake_eu" {
  env             = data.hyperfluid_env.default.id
  name            = "data-lake-eu"
  storage_zone_id = data.hyperfluid_storage_zone.eu.zone_id
}

# Look up one of the organization's object-storage zones by id. Use the
# resolved zone to place a bucket via hyperfluid_bucket.storage_zone_id.
data "hyperfluid_storage_zone" "eu" {
  zone_id = "eu-west"
}

output "zone_enabled" {
  value = data.hyperfluid_storage_zone.eu.enabled
}

output "zone_endpoint" {
  value = data.hyperfluid_storage_zone.eu.external_endpoint
}

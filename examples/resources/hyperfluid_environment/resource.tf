resource "hyperfluid_environment" "analytics" {
  name = "Analytics"
  slug = "analytics"

  # Destroying or replacing the environment deletes everything in it.
  # Set this to false and apply before you mean to.
  deletion_protection = true
}

resource "hyperfluid_bucket" "lake" {
  env  = hyperfluid_environment.analytics.id
  name = "data-lake"
}

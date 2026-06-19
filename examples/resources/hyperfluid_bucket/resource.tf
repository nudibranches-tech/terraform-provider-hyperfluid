data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_bucket" "lake" {
  env   = data.hyperfluid_env.default.id
  name     = "data-lake"
  quota_gb = 500
}

data "hyperfluid_harbor" "default" {
  name = "default"
}

resource "hyperfluid_bucket" "lake" {
  harbor   = data.hyperfluid_harbor.default.id
  name     = "data-lake"
  quota_gb = 500
}

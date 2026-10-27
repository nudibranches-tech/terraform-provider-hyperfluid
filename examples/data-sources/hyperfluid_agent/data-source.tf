data "hyperfluid_environment" "default" {
  name = "default"
}

data "hyperfluid_agent" "this" {
  env  = data.hyperfluid_environment.default.id
  name = "support"
}

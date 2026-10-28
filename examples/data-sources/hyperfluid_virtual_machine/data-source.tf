data "hyperfluid_environment" "default" {
  name = "default"
}

data "hyperfluid_virtual_machine" "web" {
  env  = data.hyperfluid_environment.default.id
  name = "web"
}

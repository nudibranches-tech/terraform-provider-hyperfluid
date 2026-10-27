data "hyperfluid_environment" "default" {
  name = "default"
}

data "hyperfluid_compute_dock" "this" {
  env  = data.hyperfluid_environment.default.id
  name = "hpc"
}

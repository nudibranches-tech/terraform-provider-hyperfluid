data "hyperfluid_environment" "default" {
  name = "default"
}

data "hyperfluid_nextflow" "this" {
  env  = data.hyperfluid_environment.default.id
  name = "genomics"
}

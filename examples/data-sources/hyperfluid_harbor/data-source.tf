data "hyperfluid_harbor" "default" {
  name = "default"
}

output "harbor_id" {
  value = data.hyperfluid_harbor.default.id
}

data "hyperfluid_environment" "default" {
  name = "default"
}

data "hyperfluid_virtual_machine_images" "catalog" {
  env = data.hyperfluid_environment.default.id
}

output "disk_images" {
  value = [for i in data.hyperfluid_virtual_machine_images.catalog.images : i.image_key if i.kind == "disk"]
}

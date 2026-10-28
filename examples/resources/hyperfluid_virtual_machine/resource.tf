data "hyperfluid_environment" "default" {
  name = "default"
}

data "hyperfluid_virtual_machine_images" "catalog" {
  env = data.hyperfluid_environment.default.id
}

locals {
  ubuntu = one([for i in data.hyperfluid_virtual_machine_images.catalog.images : i if i.image_key == "ubuntu-24.04.qcow2"])
}

# Cloud-init user-data, read once at first boot.
resource "hyperfluid_secret" "cloud_init" {
  name             = "web/cloud-init"
  secret_type      = "plaintext"
  value            = <<-YAML
    #cloud-config
    packages: [nginx]
  YAML
  value_wo_version = "1"
}

resource "hyperfluid_virtual_machine" "web" {
  env                   = data.hyperfluid_environment.default.id
  name                  = "web"
  cpu                   = "2"
  memory                = "4Gi"
  disk                  = "40Gi"
  image_source_ref      = local.ubuntu.image_source_ref
  image_key             = local.ubuntu.image_key
  user_data_secret_name = hyperfluid_secret.cloud_init.name
}

output "ip_address" {
  value = hyperfluid_virtual_machine.web.ip_address
}

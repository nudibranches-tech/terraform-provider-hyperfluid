variable "deploy_ssh_public_key" {
  type = string
}

resource "hyperfluid_secret" "deploy_ssh_key" {
  name             = "web/deploy-ssh-key"
  secret_type      = "plaintext"
  value            = var.deploy_ssh_public_key
  value_wo_version = "1"
}

# Waits for the guest agent of the virtual machine to apply the key.
resource "hyperfluid_virtual_machine_user" "deploy" {
  virtual_machine            = hyperfluid_virtual_machine.web.id
  username                   = "deploy"
  ssh_public_key_secret_name = hyperfluid_secret.deploy_ssh_key.name
}

# The generated password is kept in the secret manager, never in state.
resource "hyperfluid_virtual_machine_user" "ops" {
  virtual_machine   = hyperfluid_virtual_machine.web.id
  username          = "ops"
  generate_password = true
}

data "hyperfluid_env" "default" {
  name = "default"
}

# Reads a link declared anywhere — the console, hfctl, or another Terraform
# workspace.
data "hyperfluid_service_link" "web_to_db" {
  env  = data.hyperfluid_env.default.id
  name = "web-main"
}

output "reachable_ports" {
  value = data.hyperfluid_service_link.web_to_db.ports
}

output "link_ready" {
  value = data.hyperfluid_service_link.web_to_db.ready
}

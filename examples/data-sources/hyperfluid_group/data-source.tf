data "hyperfluid_group" "auditors" {
  name = "auditors"
}

output "auditor_ids" {
  value = [for m in data.hyperfluid_group.auditors.members : m.id]
}

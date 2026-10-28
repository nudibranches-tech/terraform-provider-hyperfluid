data "hyperfluid_environment" "default" {
  name = "default"
}

resource "hyperfluid_compute_dock" "hpc" {
  env       = data.hyperfluid_environment.default.id
  name      = "hpc"
  home_size = "100Gi"

  partitions = [
    {
      name     = "cpu"
      cpu      = "8"
      memory   = "32Gi"
      node_max = 4
      default  = true
    },
    {
      name         = "gpu"
      cpu          = "16"
      memory       = "64Gi"
      gpu          = 1
      node_max     = 2
      walltime_max = "24:00:00"
    },
  ]

  auto_suspend_idle_minutes = 60
}

output "ssh_endpoint" {
  value = hyperfluid_compute_dock.hpc.ssh_endpoint
}

data "hyperfluid_environment" "default" {
  name = "default"
}

resource "hyperfluid_nextflow" "genomics" {
  env     = data.hyperfluid_environment.default.id
  name    = "genomics"
  buckets = ["reads", "results"]

  egress = {
    fqdns = ["github.com", "*.githubusercontent.com"]
  }

  # Omit storage, head and task_quota to let the platform size them.
  storage = {
    size = "500Gi"
  }

  task_quota = {
    cpu      = "64"
    memory   = "256Gi"
    max_pods = 50
  }

  scm = {
    platform     = "github"
    token_secret = "github-token"
  }
}

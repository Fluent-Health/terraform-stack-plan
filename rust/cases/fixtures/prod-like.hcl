# Shaped like a production monorepo's .tfstackplan.hcl, with every name made up:
# classification with an IAM preset and derives, gate classes, links, the
# provider cache, the progress phase model and the cross-env uniqueness lint.
classification {
  default {
    name = "safe"
    icon = "✅"
  }
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
    # Bucket- and folder-scoped IAM carries no project attribute; recover it
    # from a bucket named "<project-id>-<suffix>".
    derive "project" {
      resource_type_pattern = "^google_storage_(bucket|managed_folder)_iam_"
      from_attribute        = "bucket"
      pattern               = "^(?:b/)?(?P<value>acme-[a-z0-9]+-(?:svc|host))-"
    }
    # Registry repo IAM names its project only inside the repository path.
    derive "project" {
      resource_type_pattern = "^google_artifact_registry_repository_iam_"
      from_attribute        = "repository"
      pattern               = "^projects/(?P<value>[^/]+)/locations/"
    }
  }
  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }

  # Notification-config creates need a topic IAM permission, so gate them
  # like IAM and recover the project from the topic path.
  rule "scc-notification" {
    icon                  = "🔐"
    resource_type_pattern = "^google_scc_v2_project_notification_config$"
    actions               = ["create"]
    emit_attributes       = ["project"]
    derive "project" {
      from_attribute = "pubsub_topic"
      pattern        = "^projects/(?P<value>[^/]+)/topics/"
    }
  }
}

class "iam" {
  backend     = "gcp-pam"
  entitlement = "example-elevation"
  required    = true
}

class "scc-notification" {
  backend     = "gcp-pam"
  entitlement = "example-elevation"
  required    = true
}

links {
  resource = "https://git.example.com/example-org/example-infra/blob/{sha}/{file}#L{line}"
  stack    = "https://git.example.com/example-org/example-infra/tree/{sha}/{stack_dir}"
  header {
    label = "Build #{build_id}"
    url   = "https://ci.example.com/builds;region={location}/{build_id}?project={project}"
  }
  header {
    label = "PR #{pr}"
    url   = "https://git.example.com/example-org/example-infra/pull/{pr}"
  }
}

cache {
  prefix = "example/tf-plugins/v2"
}

progress {
  plan {
    phase "warming" {}
    phase "linting" {}
    phase "planning" {}
    phase "classify" {}
    phase "report" {}
  }
  apply {
    phase "warming" {}
    phase "moves" {}
    phase "applying" {}
    phase "report" {}
    phase "verifying" {}
  }
}

env_uniqueness {
  protected_tier         = "prod"
  project_token_template = "acme-{env}-svc"

  source {
    tier_input = "tier_class"
  }

  allow {
    unit   = "apps/mobile/instances/mobile"
    key    = "secret_readers"
    envs   = ["prod"]
    reason = "Every environment's build authenticates as the prod CI client, so the prod reader list names the dev CI account too."
  }
  allow {
    unit   = "gateway/api/instances/api"
    key    = "metrics.endpoint_url"
    envs   = ["dev", "test", "stage", "prod"]
    reason = "One metrics stack for the whole organisation; every environment reports to the same endpoint."
  }
  allow {
    unit   = "workloads/search/instances/search"
    key    = "admin_key_secret_id"
    envs   = ["dev", "test", "stage", "prod"]
    reason = "Secret name, not a value: each project holds a secret with this name by convention."
  }
  allow {
    unit   = "workloads/tools/instances/tools"
    key    = "base_image"
    envs   = ["dev", "test", "stage", "prod"]
    reason = "Public upstream base image (`docker.io/library/postgres`); not environment-specific."
  }
  allow {
    unit   = "foundation/teams/instances/platform"
    key    = "grants.acme-nonprod-host.project_roles"
    envs   = ["dev", "prod"]
    reason = "Team baseline roles on the shared nonprod host are the same in every environment's config."
  }
  # Values identical in every environment need no allow rule: the lint only
  # flags a value duplicated across a subset of environments.
}

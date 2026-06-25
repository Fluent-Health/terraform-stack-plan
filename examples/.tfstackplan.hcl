# Canonical client policy for tfstackplan.
#
# Put this at your REPO ROOT as `.tfstackplan.hcl`. It is auto-discovered: `render`,
# `run plan`, and `run apply` walk UP from their working dir (`--dir`) to the repo
# root to find it, so commands run from a stack subdir (e.g. `run apply --dir
# stacks/prod`) pick it up with no explicit `--config`. (`serve` reads the same
# file for the class → approval-entitlement mapping.) Pass `--config` only to point
# at a non-default location.
#
# This is the shared *client* policy — classification, gating, links, progress.
# The serve runtime block (db_path, github_app, approval pool, …) lives separately;
# see `serve.tfstackplan.hcl` in this directory. Keeping them split lets a laptop
# `tfstackplan run` stay fully offline.

classification {
  default {
    name = "safe"
    icon = "✅"
  }

  # An IAM change is privileged: tag it 🔐 and emit the affected project(s) as the
  # gate target(s). `emit_attributes` names the plan attribute(s) that become the
  # per-target key; `derive` recovers it for resources that don't carry it directly.
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]

    # Example: recover the project from a bucket-scoped IAM member's bucket name
    # ("<project>-build-cache") when the resource has no `project` attribute.
    derive "project" {
      resource_type_pattern = "^google_storage_(bucket|managed_folder)_iam_"
      from_attribute        = "bucket"
      pattern               = "^(?P<value>.+)-build-cache$"
    }
  }

  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}

# Gate binding: an `iam`-classified change requires an approval grant per emitted
# target before apply. `run plan` emits the gate; `serve` maps the class to its
# approval backend + entitlement; `run apply` applies only once the gate is
# satisfied. Drop `required` (or this block) to classify-without-gating.
class "iam" {
  backend     = "gcp-pam"
  entitlement = "iam-applier-elevation"
  required    = true
}

# Optional diff policy: which attributes to expand, and per-type differs.
diff {
  detect = true
  # max_attribute_lines = 200   # optional skimmability ceiling; unset = global fit decides

  rule {
    resource_type_pattern = "^kubernetes_manifest$"
    attribute             = "manifest"
    differ                = "yaml"
  }
}

# Optional deep links rendered into the report/check run. {sha}, {file}, {line},
# {stack_dir}, {pr}, {build_id}, {location}, {project} are substituted.
links {
  resource = "https://github.com/your-org/your-repo/blob/{sha}/{file}#L{line}"
  stack    = "https://github.com/your-org/your-repo/tree/{sha}/{stack_dir}"
  header {
    label = "PR #{pr}"
    url   = "https://github.com/your-org/your-repo/pull/{pr}"
  }
}

# Optional native provider caching: pre-warm the local Terraform plugin cache
# from GCS before terraform init runs. Providers not yet in GCS are downloaded
# directly from the registry. After the script completes, newly installed providers
# are uploaded to GCS for subsequent runs. Omit this block to disable caching.
#
# Fields:
#   bucket  — GCS bucket name; also TFSTACKPLAN_CACHE_BUCKET env var
#   prefix  — GCS object key prefix (default: "infra/tf-plugins")
#   version — cache key namespace (default: "0"); bump to bust the cache
#
# cache {
#   bucket  = "my-tf-plugins-cache"
#   prefix  = "infra/tf-plugins"
#   version = "v1"
# }

# Optional full-progress phase model (serve >= v0.16.0): the ordered lifecycle
# phases each operation's CI emits, rendered as one progress bar. Keep in sync with
# the phases your plan/apply pipelines actually emit (`run phase`).
progress {
  plan {
    phase "warming" {}
    phase "linting" {}
    phase "initializing" {}
    phase "planning" {}
  }
  apply {
    phase "warming" {}
    phase "initializing" {}
    phase "applying" {}
    phase "verifying" {}
  }
}

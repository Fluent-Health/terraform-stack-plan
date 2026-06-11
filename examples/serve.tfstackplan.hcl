# serve.tfstackplan.hcl — control-plane config reference for `tfstackplan serve`.
#
# This is the same `.tfstackplan.hcl` file the `render` / `run` faces read, with
# the server-side blocks added. A render-only file needs none of these blocks;
# they are all optional and backward-compatible. Kept valid by a parse test
# (internal/config/serve_test.go: TestExampleServeConfigParses).

# --- Shared policy (also used by `render` and `run`) --------------------------

classification {
  default {
    name = "safe"
    icon = "✅"
  }

  # The `iam` preset matches IAM resources across GCP/AWS/Azure. `emit_attributes`
  # surfaces which projects had IAM changes, so the approval gate can request a
  # PAM grant per project.
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }

  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}

# --- `run` / `serve` control-plane wiring -------------------------------------

# Where this repo's CI reports to, and under which environment.
server {
  url         = "https://tfstackplan.example.com"
  environment = "nonprod"
}

# Bind a classification class to an approval gate. `required = true` means a plan
# carrying this class blocks the apply until the gate's PAM grant is ACTIVE.
# `entitlement` is the per-class PAM entitlement id; `entitlement_scope` is the
# resource scope it grants at (projects by default, or folders / organizations).
class "iam" {
  backend     = "gcp-pam"
  entitlement = "iam-elevation"
  required    = true
}

class "destructive" {
  backend           = "gcp-pam"
  entitlement       = "destructive-approval"
  entitlement_scope = "folders"
  required          = true
}

# --- The control-plane server runtime (`tfstackplan serve`) -------------------

serve {
  db_path         = "/data/tfstackplan.db"      # SQLite store (WAL; single writer)
  public_base_url = "https://tfstackplan.example.com"
  use_checks      = true                        # rich GitHub check runs (else commit statuses)

  # Env var NAME holding the bearer secret for /api/* (not the secret itself).
  # An empty/unset secret disables auth (local/dev only).
  webhook_secret_env = "TFSTACKPLAN_WEBHOOK_SECRET"

  # Per-stack on-disk log buffers (unset = log ingestion disabled).
  logs_dir = "/data/logs"

  # GitHub App credentials for posting checks/statuses and reading PR head SHAs.
  github_app {
    app_id           = "123456"
    installation_id  = "78901234"
    private_key_path = "/secrets/github-app.pem"   # PEM (PKCS#1 or PKCS#8)
  }

  # The GCP Privileged Access Manager approval backend. Per-class entitlement ids
  # come from the `class` blocks above; this block carries the shared settings.
  # Grant creation impersonates the next free identity in requester_pool (leased).
  approval "gcp-pam" {
    location       = "global"
    duration       = "28800s"   # 8h
    requester_pool = [
      "tfsp-requester-0@example.iam.gserviceaccount.com",
      "tfsp-requester-1@example.iam.gserviceaccount.com",
    ]
  }

  # Live-DAG grouping: stacks fold into group nodes by their first `depth` path
  # segments (default 2 → env/kind). `pattern` (a regexp) overrides depth — the
  # first capture group of the stack path becomes the group key.
  group {
    depth = 2
  }

  # Log offload to GCS: completed-stack logs move off the local buffer to the
  # bucket and stream back via a stored pointer (no cloud IAM for viewers).
  objects {
    backend = "gcs"
    bucket  = "tfstackplan-logs"
    prefix  = "executions"
  }

  # Pub/Sub push ingestion: an OIDC-verified low-latency alternative to the poll
  # loop. `audience` defaults to <public_base_url>/pubsub/push when unset.
  pubsub {
    audience        = "https://tfstackplan.example.com/pubsub/push"
    service_account = "pubsub-pusher@example.iam.gserviceaccount.com"
  }
}

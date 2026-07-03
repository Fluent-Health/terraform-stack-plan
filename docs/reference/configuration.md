# Configuration reference — `.tfstackplan.hcl`

All four faces (`render`, `run`, `serve`, `state`) share one HCL policy file.
Every block is optional and backward-compatible: a render-only file needs none
of the server blocks; a serve runtime config needs none of the render-only
blocks.

---

## File discovery

`render`, `run plan`, and `run apply` resolve the config file as follows:

1. If `--config FILE` is given, use that file exactly.
2. Otherwise walk **up** from the working directory (`--dir` for `run`
   subcommands, the process `cwd` for `render`) to the first ancestor that
   contains a `.git` directory. If `.tfstackplan.hcl` exists there, use it.
3. If no file is found, classification is **disabled** (no categories column or
   icons), and `diff {}` falls back to built-in defaults (type detection on, no
   per-attribute rules).

`serve` reads the file at start-up from the path given to its `--config` flag;
auto-discovery does not apply.

---

## Top-level blocks

| Block | Used by | Purpose |
|---|---|---|
| `classification {}` | `render`, `run` | Classification presets, rules, and the default label |
| `diff {}` | `render` | Differ defaults and per-attribute overrides |
| `links {}` | `render` | URL templates for header, stack, and resource deep links |
| `server {}` | `run` | Server URL and environment name for CI→server reporting |
| `class "<name>" {}` | `run`, `serve` | Bind a classification class to an approval gate |
| `progress {}` | `run`, `serve` | Ordered lifecycle phases for the progress bar |
| `serve {}` | `serve` | Control-plane runtime configuration |

---

## `classification {}` block

Drives how each stack is labelled. When the block is absent (or no config file
is found), classification is off.

### `default` sub-block

Shown when a stack matches no rule. Never appears in the sidecar JSON or the
summary (`--emit-classification-json`).

```hcl
classification {
  default {
    name = "safe"   # display name
    icon = "✅"     # optional glyph
  }
}
```

Shorthand — equivalent to `default { name = "safe" }` with no icon:

```hcl
classification {
  default = "safe"
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | — (required) | Label shown in the categories column |
| `icon` | string | none | Emoji glyph prepended to the name |

### `preset "<name>" {}` sub-block

Expands a built-in rule bundle at its declared position. Declaration order
determines badge display order.

```hcl
classification {
  preset "iam" {
    icon            = "🔐"             # optional override
    emit_attributes = ["project"]      # optional

    derive "project" {                 # optional; repeatable
      resource_type_pattern = "^google_storage_(bucket|managed_folder)_iam_"
      from_attribute        = "bucket"
      pattern               = "^(?P<value>.+)-build-cache$"
    }
  }
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `icon` | string | preset default | Override the preset's default glyph |
| `emit_attributes` | list(string) | none | Plan attribute names whose values are collected as gate targets (e.g. `["project"]`) |

`derive "<attribute>" {}` sub-blocks are described in the
[`derive` section](#derive-attribute--sub-block) below.

**Built-in presets:**

| Name | Default icon | Rule | Notes |
|---|---|---|---|
| `iam` | 🔐 | Matches IAM resource types across GCP/AWS/Azure (e.g. `_iam_(policy\|binding\|member\|audit_config)$`, `^aws_iam_`, `^azurerm_role_(assignment\|definition)$`). `actions` unset (any). | Default category name `iam` |

Unknown preset names fail at config load with a list of available presets.

### `rule "<name>" {}` sub-block

A custom rule. Evaluated independently; declaration order determines badge
display order. All matching rules fire — a stack can carry multiple categories.

```hcl
classification {
  rule "destructive" {
    icon                  = "💣"
    resource_type_pattern = ".*"       # optional
    actions               = ["delete"] # optional
    min_count             = 1          # optional
  }
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `icon` | string | none | Emoji glyph prepended to the category name |
| `resource_type_pattern` | string (regex) | `.*` | Matched against each change's resource `type` field |
| `actions` | list(string) | any | Rule fires only if **all** listed actions appear in the change's `actions[]`. Unset means any action matches |
| `min_count` | integer | `1` | Minimum number of changes matching this rule for the category to be applied to the stack |

Rule matching notes:

- Classification considers only changes that mutate the real resource:
  `add`, `change`, `destroy`, `replace`. Pure state operations (`move`,
  `import`, `forget`) never contribute to any category.
- A change that is both moved and updated classifies on its underlying `update`.
- Rules with no matcher fields are catch-alls.
- A `rule` block may also carry `derive` sub-blocks (same syntax as in
  `preset`).

### `derive "<attribute>" {}` sub-block

Recovers an emitted attribute value from a different scalar on the same
resource, for resources that don't carry the attribute directly. Runs before
the stack-level project fallback; never overrides a value the change already
carries.

```hcl
derive "project" {
  resource_type_pattern = "^google_storage_(bucket|managed_folder)_iam_"  # optional
  from_attribute        = "bucket"                                         # required
  pattern               = "^(?P<value>.+)-build-cache$"                   # required
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `resource_type_pattern` | string (regex) | `.*` | Limit derivation to matching resource types |
| `from_attribute` | string | — (required) | Source scalar attribute to read the value from |
| `pattern` | string (regex) | — (required) | Regex applied to the source value. The named capture `value` is used; if absent, group 1 is used |

---

## `diff {}` block

Controls how changed attributes are rendered. All fields are optional.

```hcl
diff {
  detect              = true    # optional; default true
  max_attribute_lines = 200     # optional; default unset (no cap)

  rule {
    resource_type_pattern = "^kubernetes_manifest$"
    attribute             = "manifest"
    differ                = "yaml"
  }
}
```

### Top-level fields

| Field | Type | Default | Description |
|---|---|---|---|
| `detect` | bool | `true` | Enable automatic type detection for JSON, YAML, and base64 attribute values |
| `max_attribute_lines` | integer | unset | Readability ceiling: when set, the differ starts at `Summary` for any `LineDiff` or `Structural` variant that would exceed this line count. Unset means the global `fit` pass is the sole size mechanism |

### `rule {}` sub-block

Forces a specific differ for a `(resource_type, attribute)` pair, overriding
auto-detection.

| Field | Type | Default | Description |
|---|---|---|---|
| `resource_type_pattern` | string (regex) | — (required) | Matched against the resource `type` |
| `attribute` | string | — (required) | Exact attribute name (or glob) |
| `differ` | string | — (required) | One of: `auto`, `structural`, `json`, `yaml`, `line`, `summary`, `hide` |

---

## `links {}` block

URL templates rendered into the report and check run. Substitution tokens:
`{sha}`, `{file}`, `{line}`, `{stack_dir}`, `{pr}`, `{build_id}`,
`{location}`, `{project}`.

```hcl
links {
  resource = "https://github.com/your-org/your-repo/blob/{sha}/{file}#L{line}"
  stack    = "https://github.com/your-org/your-repo/tree/{sha}/{stack_dir}"
  header {
    label = "PR #{pr}"
    url   = "https://github.com/your-org/your-repo/pull/{pr}"
  }
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `resource` | string | none | URL template for each changed resource, linking to its `.tf` source declaration |
| `stack` | string | none | URL template for each stack, linking to its directory in the repo |
| `header {}` | block | none | A labelled link rendered in the report header |

### `header {}` sub-block

| Field | Type | Default | Description |
|---|---|---|---|
| `label` | string | — (required) | Link text, e.g. `"PR #{pr}"` |
| `url` | string | — (required) | URL template |

---

## `server {}` block

Tells `run` subcommands where to report plan/apply events and which environment
label to use. Ignored by `render` and `serve`.

```hcl
server {
  url         = "https://tfstackplan.example.com"
  environment = "nonprod"
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `url` | string | — (required if block present) | Base URL of the `tfstackplan serve` instance |
| `environment` | string | — (required if block present) | Environment label sent with every event (e.g. `nonprod`, `prod`) |

---

## `class "<name>" {}` block

Binds a classification category to an approval gate. Used by `run` to check
whether a gate is satisfied before allowing apply, and by `serve` to know
which entitlement to request.

```hcl
class "iam" {
  backend           = "gcp-pam"
  entitlement       = "iam-applier-elevation"
  entitlement_scope = "projects"   # optional; default "projects"
  required          = true
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `backend` | string | — (required) | Approval backend identifier. Currently `"gcp-pam"` |
| `entitlement` | string | — (required) | PAM entitlement ID for this class |
| `entitlement_scope` | string | `"projects"` | Resource scope the entitlement grants at. One of `"projects"`, `"folders"`, `"organizations"` |
| `required` | bool | — (required) | When `true`, a plan carrying this class blocks apply until the gate's grant is active. When `false` (or the block is absent), the class is recorded but does not block |

The block label (e.g. `"iam"`) must match a category name produced by a
`preset` or `rule` block.

---

## `progress {}` block

Defines the ordered lifecycle phases each CI operation emits, rendered as a
single progress bar in the live viewer. Keep in sync with the phases your
pipelines actually emit via `run phase`. Requires `serve` ≥ v0.16.0.

```hcl
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
```

| Sub-block | Description |
|---|---|
| `plan {}` | Ordered phases for plan operations |
| `apply {}` | Ordered phases for apply operations |

Each `phase "<name>" {}` block declares one phase by name, in execution order.
Phase names are arbitrary strings; they must match what `run phase` emits.

---

## `serve {}` block

Runtime configuration for `tfstackplan serve`. Ignored by `render` and `run`.

```hcl
serve {
  db_path            = "/data/tfstackplan.db"
  public_base_url    = "https://tfstackplan.example.com"
  webhook_secret_env = "TFSTACKPLAN_WEBHOOK_SECRET"
  logs_dir           = "/data/logs"

  github_app {
    app_id           = "123456"
    installation_id  = "78901234"
    private_key_path = "/secrets/github-app.pem"
  }

  approval "gcp-pam" {
    location       = "global"
    duration       = "28800s"
    requester_pool = [
      "tfsp-requester-0@example.iam.gserviceaccount.com",
      "tfsp-requester-1@example.iam.gserviceaccount.com",
    ]
  }

  group   { depth = 2 }
  objects { backend = "gcs", bucket = "tfstackplan-logs", prefix = "executions" }
  pubsub  { audience = "…/pubsub/push", service_account = "…@….gserviceaccount.com" }

  api_auth {
    audience = "https://tfstackplan.example.com"
    principal "tf-planner@example.iam.gserviceaccount.com" { scopes = ["report"] }
    principal "ops@example.com"                            { scopes = ["read", "admin"] }
  }
}
```

### Top-level fields

| Field | Type | Default | Description |
|---|---|---|---|
| `db_path` | string | — (required) | Path to the SQLite database file (WAL mode; single writer by design) |
| `public_base_url` | string | — (required) | Public base URL of the serve instance, used in check-run links and as the default Pub/Sub push audience |
| `webhook_secret_env` | string | none | Name of the environment variable holding the **legacy** shared bearer secret for `/api/*` (HS256). Kept accepted alongside `api_auth {}` OIDC tokens while set. When neither this nor `api_auth {}` is configured, `/api/*` authentication is disabled (local/dev only) |
| `github_webhook_secret_env` | string | none | Name of the environment variable holding the GitHub webhook HMAC secret used to verify inbound webhook signatures |
| `logs_dir` | string | none | Directory for per-stack on-disk log buffers. When unset, log ingestion is disabled |

### `github_app {}` sub-block

GitHub App credentials for posting check runs and reading PR head SHAs.

| Field | Type | Default | Description |
|---|---|---|---|
| `app_id` | string | — (required) | GitHub App ID |
| `installation_id` | string | — (required) | Installation ID for the target organization or repository |
| `private_key_path` | string | — (required) | Path to the PEM private key file (PKCS#1 or PKCS#8) |

### `approval "<backend>" {}` sub-block

Shared settings for an approval backend. The block label is the backend
identifier and must match the `backend` field in one or more `class` blocks.

**`approval "gcp-pam" {}`:**

| Field | Type | Default | Description |
|---|---|---|---|
| `location` | string | — (required) | PAM location (e.g. `"global"`) |
| `duration` | string | — (required) | Grant duration in seconds notation (e.g. `"28800s"` for 8 h) |
| `requester_pool` | list(string) | — (required) | Service account emails leased round-robin to impersonate when requesting grants |

Grant creation uses Application Default Credentials to impersonate the next
available identity in `requester_pool`.

### `group {}` sub-block

Controls how stacks are folded into group nodes in the live DAG view.

| Field | Type | Default | Description |
|---|---|---|---|
| `depth` | integer | `2` | Group stacks by their first `depth` path segments (e.g. `2` → `env/kind`) |
| `pattern` | string (regex) | none | When set, overrides `depth`. The first capture group of the stack path becomes the group key |

`depth` and `pattern` are mutually exclusive; `pattern` takes precedence when
both are set.

### `objects {}` sub-block

GCS offload for completed-stack logs. When present, finalized per-stack logs
are moved from `logs_dir` to the bucket; the viewer streams them back via a
stored pointer without requiring cloud IAM for readers.

| Field | Type | Default | Description |
|---|---|---|---|
| `backend` | string | — (required) | Object storage backend. Currently `"gcs"` |
| `bucket` | string | — (required) | GCS bucket name |
| `prefix` | string | `""` | Key prefix for stored log objects |

### `pubsub {}` sub-block

OIDC-verified Pub/Sub push ingestion. When configured, the serve instance
accepts push messages at `/pubsub/push` as a lower-latency alternative to the
runner poll loop.

| Field | Type | Default | Description |
|---|---|---|---|
| `audience` | string | `<public_base_url>/pubsub/push` | OIDC token audience. Defaults to the push endpoint URL derived from `public_base_url` |
| `service_account` | string | — (required) | Service account email the Pub/Sub push subscription authenticates as |

### `api_auth {}` sub-block

Google OIDC bearer auth for `/api/*`. Callers present Google-signed ID tokens;
the verified email is mapped to scopes through `principal` blocks. Service
accounts (the CI runner, a UI service) mint tokens for `audience` natively;
human callers on user ADC present tokens whose audience is the fixed gcloud
client id — list it in `extra_audiences` to accept them. While
`webhook_secret_env` is also set, legacy HS256 shared-secret tokens remain
accepted (the migration posture); remove it to enforce OIDC only.

| Field | Type | Default | Description |
|---|---|---|---|
| `audience` | string | `public_base_url` | Expected ID-token audience for service-account callers |
| `extra_audiences` | list(string) | `[]` | Additional accepted audiences, e.g. `764086051850-….apps.googleusercontent.com` (the gcloud ADC user-credential client) |
| `principal "<email>" {}` | block | — | One allowlisted identity; the label is the exact (case-insensitive) verified email |

**`principal` fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `scopes` | list(string) | `[]` | Any of `report` (execution lifecycle events, logs, gate check/revoke, claims), `read` (execution state/events, claims listing), `admin` (claim release and future admin verbs) |

---

## Canonical example

`examples/.tfstackplan.hcl` is the shared client policy (classification, gating,
links, progress). The serve runtime blocks are in
`examples/serve.tfstackplan.hcl`. Both files are kept valid by parse tests.

See [`../../examples/.tfstackplan.hcl`](../../examples/.tfstackplan.hcl) and
[`../../examples/serve.tfstackplan.hcl`](../../examples/serve.tfstackplan.hcl).

---

## Related

- **Concepts:** [`../guide/05-classification.md`](../guide/05-classification.md)
  — how classification rules are evaluated, what the sidecar JSON contains, and
  how `derive` recovers emitted attributes.
- **CLI flags:** `cli.md` — the `--config` flag (all subcommands) and
  `--emit-classification-json` (`render`).

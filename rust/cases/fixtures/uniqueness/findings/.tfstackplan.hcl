env_uniqueness {
  protected_tier         = "prod"
  project_token_template = "acme-{env}"

  environment "dev"     { tier = "nonprod" }
  environment "staging" { tier = "nonprod" }
  environment "prod"    { tier = "prod" }

  extra_env_tokens = {
    staging = ["acme-stg"]
  }
  extra_scoped_segments = ["per_env"]

  allow {
    unit   = "svc/api/instances/api"
    key    = "*_url"
    envs   = ["dev", "staging", "prod"]
    reason = "shared callback endpoints, reviewed"
  }

  allow {
    unit   = "svc/api/instances/api"
    key    = "retired_id"
    envs   = ["dev", "prod"]
    reason = "kept for the migration window"
  }

  allow {
    unit    = "svc/web/instances/web"
    key     = "account_id"
    envs    = ["dev", "prod"]
    reason  = "expired exception"
    expires = "2020-01-01"
  }
}

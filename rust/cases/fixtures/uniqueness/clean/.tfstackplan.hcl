env_uniqueness {
  project_token_template = "acme-{env}"

  source {
    tier_input = "tier_class"
  }

  allow {
    unit   = "svc/api/instances/api"
    key    = "shared_secret"
    envs   = ["dev", "prod"]
    reason = "shared sandbox credential by design"
  }
}

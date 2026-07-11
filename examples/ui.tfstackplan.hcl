# tfstackplan ui — central UI service config reference.
#
# The ui face is a stateless aggregator over the tier serves: Google login for
# humans, Google OIDC service identity toward the tiers. It holds no domain
# state of its own. Kept valid by config's TestExampleUIConfigParses.

ui {
  # External base URL of this service. The OAuth redirect URI is
  # <public_base_url>/auth/callback and must match the OAuth client's
  # registered redirect exactly.
  public_base_url = "https://tfstackplan-ui-example.a.run.app"

  # Env var holding the session-cookie encryption secret (any high-entropy
  # string; rotate to invalidate all sessions).
  session_secret_env = "TFSTACKPLAN_UI_SESSION_SECRET"

  # Env var holding the GitHub App's webhook secret. The UI is the App's
  # single webhook ingress (the Re-run buttons' rerequested events): it
  # verifies GitHub's HMAC here and relays deliveries to the tiers under its
  # own Google OIDC identity — the tiers need a `webhook`-scoped principal
  # for the UI's service account, not this secret.
  github_webhook_secret_env = "TFSTACKPLAN_UI_GITHUB_WEBHOOK_SECRET"

  # The tier serves to aggregate. The UI backend mints Google OIDC ID tokens
  # for each tier's audience (default: the url) — grant its service account a
  # `read`-scoped principal in each tier's serve.api_auth block.
  tier "nonprod" {
    url = "https://tfstackplan-nonprod-example.a.run.app"
  }
  tier "prod" {
    url = "https://tfstackplan-prod-example.a.run.app"
  }

  # The Google OAuth client (Workspace-internal) users log in with. The
  # client secret rides an env var; allowed_domain is enforced against the
  # verified id_token `hd` claim.
  oauth {
    client_id         = "000000000000-example.apps.googleusercontent.com"
    client_secret_env = "TFSTACKPLAN_UI_OAUTH_SECRET"
    allowed_domain    = "example.com"

    # PAM approve/deny with the user's consented token attributes API quota
    # to the OAuth client's project — name it here (it must have the
    # Privileged Access Manager API enabled).
    quota_project = "example-svc-project"
  }
}

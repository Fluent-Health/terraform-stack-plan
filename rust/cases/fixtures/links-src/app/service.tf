resource "google_service_account" "api" {
  account_id   = "app-api"
  display_name = "API (${upper("svc")})"
}

resource "google_storage_bucket" "assets" {
  name     = "example-assets"
  location = "ASIA-SOUTH1"
  labels   = { for k, v in { app = "api" } : k => lower(v) }
}

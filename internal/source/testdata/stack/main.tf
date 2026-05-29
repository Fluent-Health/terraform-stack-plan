resource "google_storage_bucket" "state" {
  name = "x"
}

resource "google_project_iam_member" "editor" {
  role = "roles/editor"
}

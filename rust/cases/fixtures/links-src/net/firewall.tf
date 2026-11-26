resource "google_compute_firewall" "allow_iap" {
  name    = "allow-iap"
  network = google_compute_network.main.name

  allow {
    protocol = "tcp"
    ports    = ["22", "3389"]
  }

  source_ranges = ["35.235.240.0/20"]
  target_tags   = var.enabled ? ["iap"] : []
}

# Declared again: files are read in name order, so this copy wins over main.tf.
resource "google_compute_network" "main" {
  name = "shadowed"
}

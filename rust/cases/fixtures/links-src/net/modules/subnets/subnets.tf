variable "network" { type = string }
variable "ranges" { type = map(string) }

resource "google_compute_subnetwork" "this" {
  for_each      = var.ranges
  name          = "sub-${each.key}"
  network       = var.network
  region        = each.key
  ip_cidr_range = each.value

  dynamic "secondary_ip_range" {
    for_each = []
    content {
      range_name    = secondary_ip_range.value.name
      ip_cidr_range = secondary_ip_range.value.cidr
    }
  }
}

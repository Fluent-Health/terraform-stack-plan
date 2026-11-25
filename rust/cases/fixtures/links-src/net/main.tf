# Network stack: a VPC, a router and a local module for the subnets.
terraform {
  required_version = ">= 1.8"
}

locals {
  regions = ["asia-south1", "asia-south2"]
  labels = {
    team = "platform"
    env  = "test"
  }
}

resource "google_compute_network" "main" {
  name                    = "main-vpc"
  auto_create_subnetworks = false
  description             = <<-EOT
    Shared VPC for ${local.labels.env}.
    Managed by Terraform.
  EOT
}

/* The router sits after a block comment
   that spans lines. */
resource "google_compute_router" "nat" {
  for_each = toset(local.regions)
  name     = "nat-${each.key}"
  network  = google_compute_network.main.id
  region   = each.value
}

module "subnets" {
  source  = "./modules/subnets"
  network = google_compute_network.main.id
  ranges  = { for i, r in local.regions : r => cidrsubnet("10.0.0.0/16", 8, i) }
}

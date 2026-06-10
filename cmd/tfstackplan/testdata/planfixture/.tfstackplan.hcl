classification {
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }
}

class "iam" {
  backend     = "gcp-pam"
  entitlement = "iam-elev"
  required    = true
}

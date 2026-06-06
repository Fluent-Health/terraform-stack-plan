classification {
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
    derive "project" {
      resource_type_pattern = "^google_storage_(bucket|managed_folder)_iam_"
      from_attribute        = "bucket"
      pattern               = "^(?P<value>.+)-build-cache$"
    }
  }
}

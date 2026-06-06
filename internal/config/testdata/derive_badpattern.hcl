classification {
  preset "iam" {
    emit_attributes = ["project"]
    derive "project" {
      from_attribute = "bucket"
      pattern        = "^(unterminated"
    }
  }
}

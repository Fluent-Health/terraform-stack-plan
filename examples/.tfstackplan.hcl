classification {
  default {
    name = "safe"
    icon = "✅"
  }

  preset "iam" {
    icon = "🔐"
  }

  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}

diff {
  detect = true
  # max_attribute_lines = 200   # optional skimmability ceiling; unset = global fit decides

  rule {
    resource_type_pattern = "^kubernetes_manifest$"
    attribute             = "manifest"
    differ                = "yaml"
  }
}

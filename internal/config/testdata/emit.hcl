classification {
  default {
    name = "safe"
    icon = "✅"
  }

  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }

  rule "destructive" {
    icon            = "💣"
    actions         = ["delete"]
    min_count       = 1
    emit_attributes = ["name", "id"]
  }
}

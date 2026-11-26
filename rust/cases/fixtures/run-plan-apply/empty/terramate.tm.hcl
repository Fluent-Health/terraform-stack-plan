# A terramate project root outside git (required_version marks it) with the
# plan and apply scripts run plan / run apply drive, through ../fake.sh.
terramate {
  required_version = ">= 0.17.0"
  config {
    experiments = ["scripts"]
  }
}

script "plan" {
  description = "plan"
  job {
    commands = [["sh", "../../../fake.sh", "plan"]]
  }
}

script "apply" {
  description = "apply"
  job {
    commands = [["sh", "../../../fake.sh", "apply"]]
  }
}

terramate {
  config {
    experiments = ["scripts"]
  }
}

script "noop" {
  description = "no-op script for exec-layer tests"
  job {
    command = ["echo", "ran"]
  }
}

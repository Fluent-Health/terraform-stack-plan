terramate {
  config {
    experiments = ["scripts"]
  }
}

script "plan" {
  job {
    commands = [
      ["terraform", "init"],
      ["terraform", "plan"],
      ["sh", "-c", "terraform show -json > tfplan.json"],
    ]
  }
}

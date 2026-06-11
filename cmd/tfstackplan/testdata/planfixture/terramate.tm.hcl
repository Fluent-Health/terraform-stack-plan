terramate {
  config {
    experiments = ["scripts"]
  }
}

script "plan" {
  job {
    commands = [
      ["sh", "-c", "pwd | tee tfstackplan.log"],
      ["terraform", "init"],
      ["terraform", "plan"],
      ["sh", "-c", "terraform show -json > tfplan.json"],
    ]
  }
}

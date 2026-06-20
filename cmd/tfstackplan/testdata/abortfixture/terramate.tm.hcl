terramate {
  config {
    experiments = ["scripts"]
  }
}

script "apply" {
  job {
    commands = [
      ["tfstackplan", "run", "step", "--stack", "${terramate.stack.path.relative}", "--on-success", "safe", "--", "terraform", "apply", "-auto-approve", "-no-color"],
    ]
  }
}

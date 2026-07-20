terramate {
  config {
    experiments = ["scripts"]
  }
}

script "apply" {
  job {
    commands = [
      ["tfstackplan", "run", "wrap", "--stack", "${terramate.stack.path.relative}", "--on-success", "safe", "--", "terraform", "apply", "-auto-approve", "-no-color"],
    ]
  }
}

terramate {
  config {
    experiments = ["scripts"]
  }
}

script "apply" {
  job {
    commands = [
      ["terraform", "init"],
      ["sh", "-c", "terraform apply -auto-approve && touch applied"],
    ]
  }
}

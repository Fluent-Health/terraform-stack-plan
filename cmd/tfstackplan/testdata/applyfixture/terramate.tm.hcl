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

script "verify" {
  job {
    commands = [
      ["sh", "-c", "echo verified-$(basename $(pwd)) | tee tfstackplan.log; touch verified"],
    ]
  }
}

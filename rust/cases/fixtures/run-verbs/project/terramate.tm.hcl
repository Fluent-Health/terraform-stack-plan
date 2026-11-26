# A terramate project root outside git (required_version marks it) with the
# scripts the run verbs drive. No terraform: each job only echoes.
terramate {
  required_version = ">= 0.17.0"
  config {
    experiments = ["scripts"]
  }
}

script "lint" {
  description = "lint"
  job {
    commands = [["sh", "-c", "echo linted $(basename $PWD)"]]
  }
}

script "verify" {
  description = "verify"
  job {
    commands = [["sh", "-c", "echo verified $(basename $PWD) >> tfstackplan.log; echo verified $(basename $PWD)"]]
  }
}

script "broken" {
  description = "fails"
  job {
    commands = [["sh", "-c", "echo failing $(basename $PWD); exit 3"]]
  }
}

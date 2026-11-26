stack {
  name  = "worker"
  id    = "0b6a3c1e-0000-4000-8000-000000000002"
  after = ["/stacks/dev/app/api"]
  watch = ["../../../../modules/app"]
}

# Made-up control planes for the whoami/claims replay cases.
server {
  url = "https://default-plane.example"
}

server "stage" {
  url         = "https://stage-plane.example"
  environment = "staging"
}

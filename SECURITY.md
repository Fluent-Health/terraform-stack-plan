# Security

## Reporting a vulnerability

Please use [GitHub's private vulnerability reporting](https://github.com/Fluent-Health/terraform-stack-plan/security/advisories/new)
to report security issues. Do not open a public issue for a vulnerability.

We aim to acknowledge reports within 5 business days and will keep you informed
as we work toward a fix.

## Scope

tfstackplan is an offline CLI: it reads Terraform `plan.json` files and writes
markdown. It makes no network calls and requires no credentials. The most
relevant security considerations are therefore around handling untrusted plan
input (e.g. resource-exhaustion from very large plans) rather than remote
exploitation.

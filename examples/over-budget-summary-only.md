<!-- tfstackplan:nonprod -->
### Terraform plan — nonprod  (8 stacks changed)

| Stack | Add | Change | Destroy | Replace | Categories |
| --- | ---: | ---: | ---: | ---: | --- |
| data/warehouse | 0 | 0 | 6 | 0 | 🔐 iam  💣 destructive |
| networking/shared-vpc | 0 | 5 | 0 | 2 | 💣 destructive |
| observability/grafana | 5 | 6 | 0 | 0 | ✅ safe |
| platform/nonprod | 0 | 4 | 0 | 0 | 🔐 iam |
| security/secrets | 0 | 9 | 0 | 0 | 🔐 iam |
| service-projects/app-dev | 4 | 3 | 0 | 0 | ✅ safe |
| service-projects/app-prod | 0 | 6 | 0 | 0 | 🔐 iam |
| service-projects/app-test | 0 | 8 | 0 | 0 | ✅ safe |

⚠️ Per-stack detail omitted to fit GitHub's size limit (see CI logs / artifact).

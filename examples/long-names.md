<!-- tfstackplan:long-names -->
### Terraform plan — long names & for-each  (2 stacks changed)

| Stack | Change | Replace | Categories |
| --- | ---: | ---: | --- |
| platform/networking-shared-vpc · 1 import, 1 move | 5 | 0 | 🔐 iam |
| service-projects/app-prod | 2 | 1 | 🔐 iam  💣 destructive |

<details open><summary>📁&nbsp;<b>platform/networking-shared-vpc</b> · 🔐 iam · 5 change, 1 import, 1 move</summary>

>
> <details open><summary>〰️&nbsp;module.networking.module.shared_vpc.module.subnets.google_compute_subnetwork.private["us-central1-private-primary"]<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 7 → 30
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;module.platform.module.security.module.iam_bindings.google_project_iam_member.engineers["user:ivan.kerin@fluentinhealth.com"]<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
>
> <details open><summary>↪️&nbsp;module.networking.module.dns.google_dns_managed_zone.internal["internal.fh.example.com"]<br>&nbsp;&nbsp;&nbsp;&nbsp;moved from module.dns.google_dns_managed_zone.internal_fh_example_com_legacy</summary>
>
> ```diff
> (address change only)
> ```
>
> </details>
>
> <details open><summary>📥&nbsp;module.networking.module.dns.google_dns_record_set.a_records["a.internal.fh.example.com"]<br>&nbsp;&nbsp;&nbsp;&nbsp;imported<br>&nbsp;&nbsp;&nbsp;&nbsp;<sub>id=<code>projects/fh-host-nonprod/managedZones/internal-fh/rrsets/a.internal.fh.example.com./A</code></sub></summary>
>
> ```diff
> (import only)
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;module.networking.google_storage_bucket.flow_logs[""]<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 8 → 31
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_compute_address.nat<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 9 → 32
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_project_iam_member.viewers<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>

</details>

<details open><summary>📁&nbsp;<b>service-projects/app-prod</b> · 🔐 iam  💣 destructive · 2 change, 1 replace</summary>

>
> <details open><summary>〰️&nbsp;module.service_projects.module.app_prod.module.workload_identity.google_storage_bucket.runner_state["orchestration-pipeline-runner"]<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 10 → 33
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;module.service_projects.module.app_prod.module.workload_identity.google_project_iam_member.runner["serviceAccount:orchestration-runner@fh-svc-prod.iam.gserviceaccount.com"]<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
>
> <details open><summary>🔁&nbsp;module.service_projects.module.app_prod.module.compute.google_compute_instance.bastion["primary"]<br>&nbsp;&nbsp;&nbsp;&nbsp;replace</summary>
>
> ```diff
> ~ machine_type = "e2-small" → "e2-medium"
> ```
>
> </details>

</details>

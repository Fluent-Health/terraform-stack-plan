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

<details><summary>📁&nbsp;<b>data/warehouse</b> · 🔐 iam  💣 destructive · 6 destroy</summary>

>
> <details open><summary>➖&nbsp;google_project_iam_member.legacy_admins<br>&nbsp;&nbsp;&nbsp;&nbsp;2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_admins"
> ```
>
> </details>
>
> <details open><summary>➖&nbsp;google_bigquery_dataset.legacy_users<br>&nbsp;&nbsp;&nbsp;&nbsp;2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_users"
> ```
>
> </details>
>
> <details open><summary>➖&nbsp;google_storage_bucket.legacy_exports<br>&nbsp;&nbsp;&nbsp;&nbsp;2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_exports"
> ```
>
> </details>
>
> <details open><summary>➖&nbsp;google_storage_bucket.legacy_imports<br>&nbsp;&nbsp;&nbsp;&nbsp;2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_imports"
> ```
>
> </details>
>
> <details open><summary>➖&nbsp;google_pubsub_topic.legacy_stream<br>&nbsp;&nbsp;&nbsp;&nbsp;2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_stream"
> ```
>
> </details>
>
> <details open><summary>➖&nbsp;google_pubsub_subscription.legacy_sub<br>&nbsp;&nbsp;&nbsp;&nbsp;2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_sub"
> ```
>
> </details>

</details>

<details><summary>📁&nbsp;<b>networking/shared-vpc</b> · 💣 destructive · 5 change, 2 replace</summary>

>
> <details open><summary>〰️&nbsp;google_compute_subnetwork.s0<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 23 → 46
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_compute_subnetwork.s1<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 24 → 47
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_compute_subnetwork.s2<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 25 → 48
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_compute_firewall.allow_internal<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 26 → 49
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_compute_firewall.allow_health<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 27 → 50
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>🔁&nbsp;google_compute_instance.bastion<br>&nbsp;&nbsp;&nbsp;&nbsp;replace</summary>
>
> ```diff
> ~ machine_type = "e2-small" → "e2-medium"
> ```
>
> </details>
>
> <details open><summary>🔁&nbsp;google_compute_address.nat<br>&nbsp;&nbsp;&nbsp;&nbsp;replace</summary>
>
> ```diff
> ~ address_type = "INTERNAL" → "EXTERNAL"
> ```
>
> </details>

</details>

<details><summary>📁&nbsp;<b>observability/grafana</b> · ✅ safe · 5 add, 6 change</summary>

>
> <details open><summary>➕&nbsp;helm_release.grafana<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "grafana"
> ```
>
> </details>
>
> <details open><summary>➕&nbsp;helm_release.loki<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "loki"
> ```
>
> </details>
>
> <details open><summary>➕&nbsp;kubernetes_namespace.observability<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "observability"
> ```
>
> </details>
>
> <details open><summary>➕&nbsp;kubernetes_service_account.grafana<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "grafana"
> ```
>
> </details>
>
> <details open><summary>➕&nbsp;kubernetes_secret.grafana_admin<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "grafana_admin"
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;kubernetes_config_map.dashboards<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ data:
>   ~ data · text · 120 lines · 240 changed (hidden to fit size limit)
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;kubernetes_manifest.ingress<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ manifest (yaml):
>  spec:
> -    key_00: old
> -    key_01: old
> +    key_00: new
> +    key_01: new
>      key_02: old
>      key_03: old
> ```
>
> </details>
>
> <details><summary>〰️&nbsp;kubernetes_manifest.configmap<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ manifest (yaml):
>  spec:
> -    key_00: old
> -    key_01: old
> -    key_02: old
> -    key_03: old
> -    key_04: old
> -    key_05: old
> -    key_06: old
> -    key_07: old
> -    key_08: old
> -    key_09: old
> -    key_10: old
> +    key_00: new
> +    key_01: new
> +    key_02: new
> +    key_03: new
> +    key_04: new
> +    key_05: new
> +    key_06: new
> +    key_07: new
> +    key_08: new
> +    key_09: new
> +    key_10: new
>      key_11: old
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.grafana_state<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 28 → 51
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.loki_chunks<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 29 → 52
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.loki_ruler<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 30 → 53
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>

</details>

<details><summary>📁&nbsp;<b>platform/nonprod</b> · 🔐 iam · 4 change</summary>

>
> <details open><summary>〰️&nbsp;google_project_iam_member.data_engineers<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
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
>
> <details open><summary>〰️&nbsp;google_storage_bucket.tfstate<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
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
> <details open><summary>〰️&nbsp;google_storage_bucket.assets<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
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

</details>

<details><summary>📁&nbsp;<b>security/secrets</b> · 🔐 iam · 9 change</summary>

>
> <details open><summary>〰️&nbsp;google_secret_manager_secret_version.api_key<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_secret_manager_secret_version.tls_cert<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_secret_manager_secret_version.oauth_secret<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_secret_manager_secret_version.signing_key<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_project_iam_member.secret_accessors<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_project_iam_member.secret_admins<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.audit_logs<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 31 → 54
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.backups<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 32 → 55
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.archive<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 33 → 56
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>

</details>

<details><summary>📁&nbsp;<b>service-projects/app-dev</b> · ✅ safe · 4 add, 3 change</summary>

>
> <details open><summary>➕&nbsp;google_service_account.api<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "api"
> ```
>
> </details>
>
> <details open><summary>➕&nbsp;google_pubsub_topic.events<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "events"
> ```
>
> </details>
>
> <details open><summary>➕&nbsp;google_cloud_run_service.api<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "api"
> ```
>
> </details>
>
> <details open><summary>➕&nbsp;google_cloud_run_service.worker<br>&nbsp;&nbsp;&nbsp;&nbsp;3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "worker"
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.uploads<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
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
> <details open><summary>〰️&nbsp;google_storage_bucket.exports<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
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
> <details open><summary>〰️&nbsp;google_secret_manager_secret_version.db_password<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>

</details>

<details><summary>📁&nbsp;<b>service-projects/app-prod</b> · 🔐 iam · 6 change</summary>

>
> <details open><summary>〰️&nbsp;google_project_iam_member.deployers<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;kubernetes_config_map.app_config<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ data:
>   ~ data · text · 90 lines · 180 changed (hidden to fit size limit)
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.prod_state<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 19 → 42
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.prod_assets<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 20 → 43
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.prod_logs<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 21 → 44
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.prod_backups<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 22 → 45
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>

</details>

<details><summary>📁&nbsp;<b>service-projects/app-test</b> · ✅ safe · 8 change</summary>

>
> <details open><summary>〰️&nbsp;google_storage_bucket.b0<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 11 → 34
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.b1<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 12 → 35
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.b2<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 13 → 36
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.b3<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 14 → 37
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.b4<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 15 → 38
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.b5<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 16 → 39
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.b6<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 17 → 40
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_storage_bucket.b7<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 18 → 41
> ~ labels (yaml):
>  env: nonprod
> +team: platform
>  
> ```
>
> </details>

</details>

<!-- tfstackplan:nonprod -->
### Terraform plan — nonprod  (8 stacks changed)

| Stack | Add | Change | Destroy | Replace | Class |
| --- | ---: | ---: | ---: | ---: | --- |
| platform/nonprod | 0 | 4 | 0 | 0 | 🔐 iam |
| service-projects/app-dev | 4 | 3 | 0 | 0 | ✅ safe |
| service-projects/app-test | 0 | 8 | 0 | 0 | ✅ safe |
| service-projects/app-prod | 0 | 6 | 0 | 0 | 🔐 iam |
| data/warehouse | 0 | 0 | 6 | 0 | 💣 destructive |
| networking/shared-vpc | 0 | 5 | 0 | 2 | 💣 destructive |
| observability/grafana | 5 | 6 | 0 | 0 | ✅ safe |
| security/secrets | 0 | 9 | 0 | 0 | 🔐 iam |

<details><summary>platform/nonprod · 🔐 iam · 4 change</summary>

> <details open><summary>~ google_project_iam_member.data_engineers · 1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
> <details open><summary>~ google_project_iam_member.viewers · 1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.tfstate · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 7 → 30
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.assets · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 8 → 31
> ```
>
> </details>

</details>

<details><summary>service-projects/app-dev · ✅ safe · 4 add, 3 change</summary>

> <details open><summary>+ google_service_account.api · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "api"
> ```
>
> </details>
> <details open><summary>+ google_pubsub_topic.events · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "events"
> ```
>
> </details>
> <details open><summary>+ google_cloud_run_service.api · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "api"
> ```
>
> </details>
> <details open><summary>+ google_cloud_run_service.worker · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "worker"
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.uploads · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 9 → 32
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.exports · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 10 → 33
> ```
>
> </details>
> <details open><summary>~ google_secret_manager_secret_version.db_password · 1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>

</details>

<details><summary>service-projects/app-test · ✅ safe · 8 change</summary>

> <details open><summary>~ google_storage_bucket.b0 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 11 → 34
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.b1 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 12 → 35
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.b2 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 13 → 36
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.b3 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 14 → 37
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.b4 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 15 → 38
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.b5 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 16 → 39
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.b6 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 17 → 40
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.b7 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 18 → 41
> ```
>
> </details>

</details>

<details><summary>service-projects/app-prod · 🔐 iam · 6 change</summary>

> <details open><summary>~ google_project_iam_member.deployers · 1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
> <details open><summary>~ kubernetes_config_map.app_config · 1 changed</summary>
>
> ```diff
> ~ data:
>   ~ data · text · 90 lines · 180 changed (hidden to fit size limit)
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.prod_state · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 19 → 42
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.prod_assets · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 20 → 43
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.prod_logs · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 21 → 44
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.prod_backups · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 22 → 45
> ```
>
> </details>

</details>

<details><summary>data/warehouse · 💣 destructive · 6 destroy</summary>

> <details open><summary>- google_bigquery_dataset.legacy_events · 2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_events"
> ```
>
> </details>
> <details open><summary>- google_bigquery_dataset.legacy_users · 2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_users"
> ```
>
> </details>
> <details open><summary>- google_storage_bucket.legacy_exports · 2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_exports"
> ```
>
> </details>
> <details open><summary>- google_storage_bucket.legacy_imports · 2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_imports"
> ```
>
> </details>
> <details open><summary>- google_pubsub_topic.legacy_stream · 2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_stream"
> ```
>
> </details>
> <details open><summary>- google_pubsub_subscription.legacy_sub · 2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_sub"
> ```
>
> </details>

</details>

<details><summary>networking/shared-vpc · 💣 destructive · 5 change, 2 replace</summary>

> <details open><summary>~ google_compute_subnetwork.s0 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 23 → 46
> ```
>
> </details>
> <details open><summary>~ google_compute_subnetwork.s1 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 24 → 47
> ```
>
> </details>
> <details open><summary>~ google_compute_subnetwork.s2 · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 25 → 48
> ```
>
> </details>
> <details open><summary>~ google_compute_firewall.allow_internal · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 26 → 49
> ```
>
> </details>
> <details open><summary>~ google_compute_firewall.allow_health · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 27 → 50
> ```
>
> </details>
> <details open><summary>± google_compute_instance.bastion · replace</summary>
>
> ```diff
> ~ machine_type = "e2-small" → "e2-medium"
> ```
>
> </details>
> <details open><summary>± google_compute_address.nat · replace</summary>
>
> ```diff
> ~ address_type = "INTERNAL" → "EXTERNAL"
> ```
>
> </details>

</details>

<details><summary>observability/grafana · ✅ safe · 5 add, 6 change</summary>

> <details open><summary>+ helm_release.grafana · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "grafana"
> ```
>
> </details>
> <details open><summary>+ helm_release.loki · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "loki"
> ```
>
> </details>
> <details open><summary>+ kubernetes_namespace.observability · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "observability"
> ```
>
> </details>
> <details open><summary>+ kubernetes_service_account.grafana · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "grafana"
> ```
>
> </details>
> <details open><summary>+ kubernetes_secret.grafana_admin · 3 attrs</summary>
>
> ```diff
> + disabled = false
> + location = "us-central1"
> + name     = "grafana_admin"
> ```
>
> </details>
> <details open><summary>~ kubernetes_config_map.dashboards · 1 changed</summary>
>
> ```diff
> ~ data:
>   ~ data · text · 120 lines · 240 changed (hidden to fit size limit)
> ```
>
> </details>
> <details open><summary>~ kubernetes_manifest.ingress · 1 changed</summary>
>
> ```diff
> ~ manifest.spec.key_00 = "old" → "new"
> ~ manifest.spec.key_01 = "old" → "new"
> ```
>
> </details>
> <details><summary>~ kubernetes_manifest.configmap · 1 changed</summary>
>
> ```diff
> ~ manifest:
>   ~ spec.key_00: old -> new
>   ~ spec.key_01: old -> new
>   ~ spec.key_02: old -> new
>   ~ spec.key_03: old -> new
>   ~ spec.key_04: old -> new
>   ~ spec.key_05: old -> new
>   ~ spec.key_06: old -> new
>   ~ spec.key_07: old -> new
>   ~ spec.key_08: old -> new
>   ~ spec.key_09: old -> new
>   ~ spec.key_10: old -> new
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.grafana_state · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 28 → 51
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.loki_chunks · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 29 → 52
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.loki_ruler · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 30 → 53
> ```
>
> </details>

</details>

<details><summary>security/secrets · 🔐 iam · 9 change</summary>

> <details open><summary>~ google_secret_manager_secret_version.api_key · 1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>
> <details open><summary>~ google_secret_manager_secret_version.tls_cert · 1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>
> <details open><summary>~ google_secret_manager_secret_version.oauth_secret · 1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>
> <details open><summary>~ google_secret_manager_secret_version.signing_key · 1 changed</summary>
>
> ```diff
> ~ secret_data = (sensitive value)
> ```
>
> </details>
> <details open><summary>~ google_project_iam_member.secret_accessors · 1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
> <details open><summary>~ google_project_iam_member.secret_admins · 1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.audit_logs · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 31 → 54
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.backups · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 32 → 55
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.archive · 2 changed</summary>
>
> ```diff
> + labels.team    = "platform"
> ~ retention_days = 33 → 56
> ```
>
> </details>

</details>

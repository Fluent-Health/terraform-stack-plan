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
> <details><summary>~ kubernetes_config_map.app_config · 1 changed</summary>
>
> ```diff
> ~ data:
>   -statement_0000 allow action old
>   -statement_0001 allow action old
>   -statement_0002 allow action old
>   -statement_0003 allow action old
>   -statement_0004 allow action old
>   -statement_0005 allow action old
>   -statement_0006 allow action old
>   -statement_0007 allow action old
>   -statement_0008 allow action old
>   -statement_0009 allow action old
>   -statement_0010 allow action old
>   -statement_0011 allow action old
>   -statement_0012 allow action old
>   -statement_0013 allow action old
>   -statement_0014 allow action old
>   -statement_0015 allow action old
>   -statement_0016 allow action old
>   -statement_0017 allow action old
>   -statement_0018 allow action old
>   -statement_0019 allow action old
>   -statement_0020 allow action old
>   -statement_0021 allow action old
>   -statement_0022 allow action old
>   -statement_0023 allow action old
>   -statement_0024 allow action old
>   -statement_0025 allow action old
>   -statement_0026 allow action old
>   -statement_0027 allow action old
>   -statement_0028 allow action old
>   -statement_0029 allow action old
>   -statement_0030 allow action old
>   -statement_0031 allow action old
>   -statement_0032 allow action old
>   -statement_0033 allow action old
>   -statement_0034 allow action old
>   -statement_0035 allow action old
>   -statement_0036 allow action old
>   -statement_0037 allow action old
>   -statement_0038 allow action old
>   -statement_0039 allow action old
>   -statement_0040 allow action old
>   -statement_0041 allow action old
>   -statement_0042 allow action old
>   -statement_0043 allow action old
>   -statement_0044 allow action old
>   -statement_0045 allow action old
>   -statement_0046 allow action old
>   -statement_0047 allow action old
>   -statement_0048 allow action old
>   -statement_0049 allow action old
>   -statement_0050 allow action old
>   -statement_0051 allow action old
>   -statement_0052 allow action old
>   -statement_0053 allow action old
>   -statement_0054 allow action old
>   -statement_0055 allow action old
>   -statement_0056 allow action old
>   -statement_0057 allow action old
>   -statement_0058 allow action old
>   -statement_0059 allow action old
>   -statement_0060 allow action old
>   -statement_0061 allow action old
>   -statement_0062 allow action old
>   -statement_0063 allow action old
>   -statement_0064 allow action old
>   -statement_0065 allow action old
>   -statement_0066 allow action old
>   -statement_0067 allow action old
>   -statement_0068 allow action old
>   -statement_0069 allow action old
>   -statement_0070 allow action old
>   -statement_0071 allow action old
>   -statement_0072 allow action old
>   -statement_0073 allow action old
>   -statement_0074 allow action old
>   -statement_0075 allow action old
>   -statement_0076 allow action old
>   -statement_0077 allow action old
>   -statement_0078 allow action old
>   -statement_0079 allow action old
>   -statement_0080 allow action old
>   -statement_0081 allow action old
>   -statement_0082 allow action old
>   -statement_0083 allow action old
>   -statement_0084 allow action old
>   -statement_0085 allow action old
>   -statement_0086 allow action old
>   -statement_0087 allow action old
>   -statement_0088 allow action old
>   -statement_0089 allow action old
>   +statement_0000 allow action new
>   +statement_0001 allow action new
>   +statement_0002 allow action new
>   +statement_0003 allow action new
>   +statement_0004 allow action new
>   +statement_0005 allow action new
>   +statement_0006 allow action new
>   +statement_0007 allow action new
>   +statement_0008 allow action new
>   +statement_0009 allow action new
>   +statement_0010 allow action new
>   +statement_0011 allow action new
>   +statement_0012 allow action new
>   +statement_0013 allow action new
>   +statement_0014 allow action new
>   +statement_0015 allow action new
>   +statement_0016 allow action new
>   +statement_0017 allow action new
>   +statement_0018 allow action new
>   +statement_0019 allow action new
>   +statement_0020 allow action new
>   +statement_0021 allow action new
>   +statement_0022 allow action new
>   +statement_0023 allow action new
>   +statement_0024 allow action new
>   +statement_0025 allow action new
>   +statement_0026 allow action new
>   +statement_0027 allow action new
>   +statement_0028 allow action new
>   +statement_0029 allow action new
>   +statement_0030 allow action new
>   +statement_0031 allow action new
>   +statement_0032 allow action new
>   +statement_0033 allow action new
>   +statement_0034 allow action new
>   +statement_0035 allow action new
>   +statement_0036 allow action new
>   +statement_0037 allow action new
>   +statement_0038 allow action new
>   +statement_0039 allow action new
>   +statement_0040 allow action new
>   +statement_0041 allow action new
>   +statement_0042 allow action new
>   +statement_0043 allow action new
>   +statement_0044 allow action new
>   +statement_0045 allow action new
>   +statement_0046 allow action new
>   +statement_0047 allow action new
>   +statement_0048 allow action new
>   +statement_0049 allow action new
>   +statement_0050 allow action new
>   +statement_0051 allow action new
>   +statement_0052 allow action new
>   +statement_0053 allow action new
>   +statement_0054 allow action new
>   +statement_0055 allow action new
>   +statement_0056 allow action new
>   +statement_0057 allow action new
>   +statement_0058 allow action new
>   +statement_0059 allow action new
>   +statement_0060 allow action new
>   +statement_0061 allow action new
>   +statement_0062 allow action new
>   +statement_0063 allow action new
>   +statement_0064 allow action new
>   +statement_0065 allow action new
>   +statement_0066 allow action new
>   +statement_0067 allow action new
>   +statement_0068 allow action new
>   +statement_0069 allow action new
>   +statement_0070 allow action new
>   +statement_0071 allow action new
>   +statement_0072 allow action new
>   +statement_0073 allow action new
>   +statement_0074 allow action new
>   +statement_0075 allow action new
>   +statement_0076 allow action new
>   +statement_0077 allow action new
>   +statement_0078 allow action new
>   +statement_0079 allow action new
>   +statement_0080 allow action new
>   +statement_0081 allow action new
>   +statement_0082 allow action new
>   +statement_0083 allow action new
>   +statement_0084 allow action new
>   +statement_0085 allow action new
>   +statement_0086 allow action new
>   +statement_0087 allow action new
>   +statement_0088 allow action new
>   +statement_0089 allow action new
>    
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
> <details><summary>~ kubernetes_config_map.dashboards · 1 changed</summary>
>
> ```diff
> ~ data:
>   -statement_0000 allow action old
>   -statement_0001 allow action old
>   -statement_0002 allow action old
>   -statement_0003 allow action old
>   -statement_0004 allow action old
>   -statement_0005 allow action old
>   -statement_0006 allow action old
>   -statement_0007 allow action old
>   -statement_0008 allow action old
>   -statement_0009 allow action old
>   -statement_0010 allow action old
>   -statement_0011 allow action old
>   -statement_0012 allow action old
>   -statement_0013 allow action old
>   -statement_0014 allow action old
>   -statement_0015 allow action old
>   -statement_0016 allow action old
>   -statement_0017 allow action old
>   -statement_0018 allow action old
>   -statement_0019 allow action old
>   -statement_0020 allow action old
>   -statement_0021 allow action old
>   -statement_0022 allow action old
>   -statement_0023 allow action old
>   -statement_0024 allow action old
>   -statement_0025 allow action old
>   -statement_0026 allow action old
>   -statement_0027 allow action old
>   -statement_0028 allow action old
>   -statement_0029 allow action old
>   -statement_0030 allow action old
>   -statement_0031 allow action old
>   -statement_0032 allow action old
>   -statement_0033 allow action old
>   -statement_0034 allow action old
>   -statement_0035 allow action old
>   -statement_0036 allow action old
>   -statement_0037 allow action old
>   -statement_0038 allow action old
>   -statement_0039 allow action old
>   -statement_0040 allow action old
>   -statement_0041 allow action old
>   -statement_0042 allow action old
>   -statement_0043 allow action old
>   -statement_0044 allow action old
>   -statement_0045 allow action old
>   -statement_0046 allow action old
>   -statement_0047 allow action old
>   -statement_0048 allow action old
>   -statement_0049 allow action old
>   -statement_0050 allow action old
>   -statement_0051 allow action old
>   -statement_0052 allow action old
>   -statement_0053 allow action old
>   -statement_0054 allow action old
>   -statement_0055 allow action old
>   -statement_0056 allow action old
>   -statement_0057 allow action old
>   -statement_0058 allow action old
>   -statement_0059 allow action old
>   -statement_0060 allow action old
>   -statement_0061 allow action old
>   -statement_0062 allow action old
>   -statement_0063 allow action old
>   -statement_0064 allow action old
>   -statement_0065 allow action old
>   -statement_0066 allow action old
>   -statement_0067 allow action old
>   -statement_0068 allow action old
>   -statement_0069 allow action old
>   -statement_0070 allow action old
>   -statement_0071 allow action old
>   -statement_0072 allow action old
>   -statement_0073 allow action old
>   -statement_0074 allow action old
>   -statement_0075 allow action old
>   -statement_0076 allow action old
>   -statement_0077 allow action old
>   -statement_0078 allow action old
>   -statement_0079 allow action old
>   -statement_0080 allow action old
>   -statement_0081 allow action old
>   -statement_0082 allow action old
>   -statement_0083 allow action old
>   -statement_0084 allow action old
>   -statement_0085 allow action old
>   -statement_0086 allow action old
>   -statement_0087 allow action old
>   -statement_0088 allow action old
>   -statement_0089 allow action old
>   -statement_0090 allow action old
>   -statement_0091 allow action old
>   -statement_0092 allow action old
>   -statement_0093 allow action old
>   -statement_0094 allow action old
>   -statement_0095 allow action old
>   -statement_0096 allow action old
>   -statement_0097 allow action old
>   -statement_0098 allow action old
>   -statement_0099 allow action old
>   -statement_0100 allow action old
>   -statement_0101 allow action old
>   -statement_0102 allow action old
>   -statement_0103 allow action old
>   -statement_0104 allow action old
>   -statement_0105 allow action old
>   -statement_0106 allow action old
>   -statement_0107 allow action old
>   -statement_0108 allow action old
>   -statement_0109 allow action old
>   -statement_0110 allow action old
>   -statement_0111 allow action old
>   -statement_0112 allow action old
>   -statement_0113 allow action old
>   -statement_0114 allow action old
>   -statement_0115 allow action old
>   -statement_0116 allow action old
>   -statement_0117 allow action old
>   -statement_0118 allow action old
>   -statement_0119 allow action old
>   +statement_0000 allow action new
>   +statement_0001 allow action new
>   +statement_0002 allow action new
>   +statement_0003 allow action new
>   +statement_0004 allow action new
>   +statement_0005 allow action new
>   +statement_0006 allow action new
>   +statement_0007 allow action new
>   +statement_0008 allow action new
>   +statement_0009 allow action new
>   +statement_0010 allow action new
>   +statement_0011 allow action new
>   +statement_0012 allow action new
>   +statement_0013 allow action new
>   +statement_0014 allow action new
>   +statement_0015 allow action new
>   +statement_0016 allow action new
>   +statement_0017 allow action new
>   +statement_0018 allow action new
>   +statement_0019 allow action new
>   +statement_0020 allow action new
>   +statement_0021 allow action new
>   +statement_0022 allow action new
>   +statement_0023 allow action new
>   +statement_0024 allow action new
>   +statement_0025 allow action new
>   +statement_0026 allow action new
>   +statement_0027 allow action new
>   +statement_0028 allow action new
>   +statement_0029 allow action new
>   +statement_0030 allow action new
>   +statement_0031 allow action new
>   +statement_0032 allow action new
>   +statement_0033 allow action new
>   +statement_0034 allow action new
>   +statement_0035 allow action new
>   +statement_0036 allow action new
>   +statement_0037 allow action new
>   +statement_0038 allow action new
>   +statement_0039 allow action new
>   +statement_0040 allow action new
>   +statement_0041 allow action new
>   +statement_0042 allow action new
>   +statement_0043 allow action new
>   +statement_0044 allow action new
>   +statement_0045 allow action new
>   +statement_0046 allow action new
>   +statement_0047 allow action new
>   +statement_0048 allow action new
>   +statement_0049 allow action new
>   +statement_0050 allow action new
>   +statement_0051 allow action new
>   +statement_0052 allow action new
>   +statement_0053 allow action new
>   +statement_0054 allow action new
>   +statement_0055 allow action new
>   +statement_0056 allow action new
>   +statement_0057 allow action new
>   +statement_0058 allow action new
>   +statement_0059 allow action new
>   +statement_0060 allow action new
>   +statement_0061 allow action new
>   +statement_0062 allow action new
>   +statement_0063 allow action new
>   +statement_0064 allow action new
>   +statement_0065 allow action new
>   +statement_0066 allow action new
>   +statement_0067 allow action new
>   +statement_0068 allow action new
>   +statement_0069 allow action new
>   +statement_0070 allow action new
>   +statement_0071 allow action new
>   +statement_0072 allow action new
>   +statement_0073 allow action new
>   +statement_0074 allow action new
>   +statement_0075 allow action new
>   +statement_0076 allow action new
>   +statement_0077 allow action new
>   +statement_0078 allow action new
>   +statement_0079 allow action new
>   +statement_0080 allow action new
>   +statement_0081 allow action new
>   +statement_0082 allow action new
>   +statement_0083 allow action new
>   +statement_0084 allow action new
>   +statement_0085 allow action new
>   +statement_0086 allow action new
>   +statement_0087 allow action new
>   +statement_0088 allow action new
>   +statement_0089 allow action new
>   +statement_0090 allow action new
>   +statement_0091 allow action new
>   +statement_0092 allow action new
>   +statement_0093 allow action new
>   +statement_0094 allow action new
>   +statement_0095 allow action new
>   +statement_0096 allow action new
>   +statement_0097 allow action new
>   +statement_0098 allow action new
>   +statement_0099 allow action new
>   +statement_0100 allow action new
>   +statement_0101 allow action new
>   +statement_0102 allow action new
>   +statement_0103 allow action new
>   +statement_0104 allow action new
>   +statement_0105 allow action new
>   +statement_0106 allow action new
>   +statement_0107 allow action new
>   +statement_0108 allow action new
>   +statement_0109 allow action new
>   +statement_0110 allow action new
>   +statement_0111 allow action new
>   +statement_0112 allow action new
>   +statement_0113 allow action new
>   +statement_0114 allow action new
>   +statement_0115 allow action new
>   +statement_0116 allow action new
>   +statement_0117 allow action new
>   +statement_0118 allow action new
>   +statement_0119 allow action new
>    
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

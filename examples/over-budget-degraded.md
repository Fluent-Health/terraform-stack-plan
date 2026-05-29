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

```diff
# google_project_iam_member.data_engineers
~ role = "roles/viewer" → "roles/editor"
```

```diff
# google_project_iam_member.viewers
~ role = "roles/viewer" → "roles/editor"
```

```diff
# google_storage_bucket.tfstate
+ labels.team    = "platform"
~ retention_days = 7 → 30
```

```diff
# google_storage_bucket.assets
+ labels.team    = "platform"
~ retention_days = 8 → 31
```
</details>

<details><summary>service-projects/app-dev · ✅ safe · 4 add, 3 change</summary>

<details><summary>+ google_service_account.api · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "api"
```

</details>

<details><summary>+ google_pubsub_topic.events · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "events"
```

</details>

<details><summary>+ google_cloud_run_service.api · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "api"
```

</details>

<details><summary>+ google_cloud_run_service.worker · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "worker"
```

</details>

```diff
# google_storage_bucket.uploads
+ labels.team    = "platform"
~ retention_days = 9 → 32
```

```diff
# google_storage_bucket.exports
+ labels.team    = "platform"
~ retention_days = 10 → 33
```

```diff
# google_secret_manager_secret_version.db_password
~ secret_data = (sensitive value)
```
</details>

<details><summary>service-projects/app-test · ✅ safe · 8 change</summary>

```diff
# google_storage_bucket.b0
+ labels.team    = "platform"
~ retention_days = 11 → 34
```

```diff
# google_storage_bucket.b1
+ labels.team    = "platform"
~ retention_days = 12 → 35
```

```diff
# google_storage_bucket.b2
+ labels.team    = "platform"
~ retention_days = 13 → 36
```

```diff
# google_storage_bucket.b3
+ labels.team    = "platform"
~ retention_days = 14 → 37
```

```diff
# google_storage_bucket.b4
+ labels.team    = "platform"
~ retention_days = 15 → 38
```

```diff
# google_storage_bucket.b5
+ labels.team    = "platform"
~ retention_days = 16 → 39
```

```diff
# google_storage_bucket.b6
+ labels.team    = "platform"
~ retention_days = 17 → 40
```

```diff
# google_storage_bucket.b7
+ labels.team    = "platform"
~ retention_days = 18 → 41
```
</details>

<details><summary>service-projects/app-prod · 🔐 iam · 6 change</summary>

```diff
# google_project_iam_member.deployers
~ role = "roles/viewer" → "roles/editor"
```

```diff
# kubernetes_config_map.app_config
```

<details><summary>~ data · 1 lines</summary>

```diff
  ~ data · text · 90 lines · 180 changed (hidden to fit size limit)
```

</details>

```diff
# google_storage_bucket.prod_state
+ labels.team    = "platform"
~ retention_days = 19 → 42
```

```diff
# google_storage_bucket.prod_assets
+ labels.team    = "platform"
~ retention_days = 20 → 43
```

```diff
# google_storage_bucket.prod_logs
+ labels.team    = "platform"
~ retention_days = 21 → 44
```

```diff
# google_storage_bucket.prod_backups
+ labels.team    = "platform"
~ retention_days = 22 → 45
```
</details>

<details><summary>data/warehouse · 💣 destructive · 6 destroy</summary>

<details><summary>- google_bigquery_dataset.legacy_events · 2 attrs</summary>

```diff
- location = "us-central1"
- name     = "legacy_events"
```

</details>

<details><summary>- google_bigquery_dataset.legacy_users · 2 attrs</summary>

```diff
- location = "us-central1"
- name     = "legacy_users"
```

</details>

<details><summary>- google_storage_bucket.legacy_exports · 2 attrs</summary>

```diff
- location = "us-central1"
- name     = "legacy_exports"
```

</details>

<details><summary>- google_storage_bucket.legacy_imports · 2 attrs</summary>

```diff
- location = "us-central1"
- name     = "legacy_imports"
```

</details>

<details><summary>- google_pubsub_topic.legacy_stream · 2 attrs</summary>

```diff
- location = "us-central1"
- name     = "legacy_stream"
```

</details>

<details><summary>- google_pubsub_subscription.legacy_sub · 2 attrs</summary>

```diff
- location = "us-central1"
- name     = "legacy_sub"
```

</details>
</details>

<details><summary>networking/shared-vpc · 💣 destructive · 5 change, 2 replace</summary>

```diff
# google_compute_subnetwork.s0
+ labels.team    = "platform"
~ retention_days = 23 → 46
```

```diff
# google_compute_subnetwork.s1
+ labels.team    = "platform"
~ retention_days = 24 → 47
```

```diff
# google_compute_subnetwork.s2
+ labels.team    = "platform"
~ retention_days = 25 → 48
```

```diff
# google_compute_firewall.allow_internal
+ labels.team    = "platform"
~ retention_days = 26 → 49
```

```diff
# google_compute_firewall.allow_health
+ labels.team    = "platform"
~ retention_days = 27 → 50
```

```diff
# google_compute_instance.bastion · replace
~ machine_type = "e2-small" → "e2-medium"
```

```diff
# google_compute_address.nat · replace
~ address_type = "INTERNAL" → "EXTERNAL"
```
</details>

<details><summary>observability/grafana · ✅ safe · 5 add, 6 change</summary>

<details><summary>+ helm_release.grafana · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "grafana"
```

</details>

<details><summary>+ helm_release.loki · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "loki"
```

</details>

<details><summary>+ kubernetes_namespace.observability · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "observability"
```

</details>

<details><summary>+ kubernetes_service_account.grafana · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "grafana"
```

</details>

<details><summary>+ kubernetes_secret.grafana_admin · 3 attrs</summary>

```diff
+ disabled = false
+ location = "us-central1"
+ name     = "grafana_admin"
```

</details>

```diff
# kubernetes_config_map.dashboards
```

<details><summary>~ data · 1 lines</summary>

```diff
  ~ data · text · 120 lines · 240 changed (hidden to fit size limit)
```

</details>

```diff
# kubernetes_manifest.ingress
~ manifest.spec.key_00 = "old" → "new"
~ manifest.spec.key_01 = "old" → "new"
```

```diff
# kubernetes_manifest.configmap
```

<details><summary>~ manifest · 11 lines</summary>

```diff
  ~ spec.key_00: old -> new
  ~ spec.key_01: old -> new
  ~ spec.key_02: old -> new
  ~ spec.key_03: old -> new
  ~ spec.key_04: old -> new
  ~ spec.key_05: old -> new
  ~ spec.key_06: old -> new
  ~ spec.key_07: old -> new
  ~ spec.key_08: old -> new
  ~ spec.key_09: old -> new
  ~ spec.key_10: old -> new
```

</details>

```diff
# google_storage_bucket.grafana_state
+ labels.team    = "platform"
~ retention_days = 28 → 51
```

```diff
# google_storage_bucket.loki_chunks
+ labels.team    = "platform"
~ retention_days = 29 → 52
```

```diff
# google_storage_bucket.loki_ruler
+ labels.team    = "platform"
~ retention_days = 30 → 53
```
</details>

<details><summary>security/secrets · 🔐 iam · 9 change</summary>

```diff
# google_secret_manager_secret_version.api_key
~ secret_data = (sensitive value)
```

```diff
# google_secret_manager_secret_version.tls_cert
~ secret_data = (sensitive value)
```

```diff
# google_secret_manager_secret_version.oauth_secret
~ secret_data = (sensitive value)
```

```diff
# google_secret_manager_secret_version.signing_key
~ secret_data = (sensitive value)
```

```diff
# google_project_iam_member.secret_accessors
~ role = "roles/viewer" → "roles/editor"
```

```diff
# google_project_iam_member.secret_admins
~ role = "roles/viewer" → "roles/editor"
```

```diff
# google_storage_bucket.audit_logs
+ labels.team    = "platform"
~ retention_days = 31 → 54
```

```diff
# google_storage_bucket.backups
+ labels.team    = "platform"
~ retention_days = 32 → 55
```

```diff
# google_storage_bucket.archive
+ labels.team    = "platform"
~ retention_days = 33 → 56
```
</details>

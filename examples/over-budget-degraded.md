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
| observability/grafana | 5 | 4 | 0 | 0 | ✅ safe |
| security/secrets | 0 | 9 | 0 | 0 | 🔐 iam |

<details><summary>platform/nonprod · 🔐 iam · 4 change</summary>

```diff
# google_project_iam_member.data_engineers will be updated in-place
~ role: "roles/viewer" -> "roles/editor"

# google_project_iam_member.viewers will be updated in-place
~ role: "roles/viewer" -> "roles/editor"

# google_storage_bucket.tfstate will be updated in-place
  + team: platform
~ retention_days: 7 -> 30

# google_storage_bucket.assets will be updated in-place
  + team: platform
~ retention_days: 8 -> 31

```

</details>

<details><summary>service-projects/app-dev · ✅ safe · 4 add, 3 change</summary>

```diff
+ google_service_account.api
+ google_pubsub_topic.events
+ google_cloud_run_service.api
+ google_cloud_run_service.worker
# google_storage_bucket.uploads will be updated in-place
  + team: platform
~ retention_days: 9 -> 32

# google_storage_bucket.exports will be updated in-place
  + team: platform
~ retention_days: 10 -> 33

# google_secret_manager_secret_version.db_password will be updated in-place
~ secret_data: (sensitive value)

```

</details>

<details><summary>service-projects/app-test · ✅ safe · 8 change</summary>

```diff
# google_storage_bucket.b0 will be updated in-place
  + team: platform
~ retention_days: 11 -> 34

# google_storage_bucket.b1 will be updated in-place
  + team: platform
~ retention_days: 12 -> 35

# google_storage_bucket.b2 will be updated in-place
  + team: platform
~ retention_days: 13 -> 36

# google_storage_bucket.b3 will be updated in-place
  + team: platform
~ retention_days: 14 -> 37

# google_storage_bucket.b4 will be updated in-place
  + team: platform
~ retention_days: 15 -> 38

# google_storage_bucket.b5 will be updated in-place
  + team: platform
~ retention_days: 16 -> 39

# google_storage_bucket.b6 will be updated in-place
  + team: platform
~ retention_days: 17 -> 40

# google_storage_bucket.b7 will be updated in-place
  + team: platform
~ retention_days: 18 -> 41

```

</details>

<details><summary>service-projects/app-prod · 🔐 iam · 6 change</summary>

```diff
# google_project_iam_member.deployers will be updated in-place
~ role: "roles/viewer" -> "roles/editor"

# kubernetes_config_map.app_config will be updated in-place
  ~ data · text · 90 lines · 180 changed (hidden to fit size limit)

# google_storage_bucket.prod_state will be updated in-place
  + team: platform
~ retention_days: 19 -> 42

# google_storage_bucket.prod_assets will be updated in-place
  + team: platform
~ retention_days: 20 -> 43

# google_storage_bucket.prod_logs will be updated in-place
  + team: platform
~ retention_days: 21 -> 44

# google_storage_bucket.prod_backups will be updated in-place
  + team: platform
~ retention_days: 22 -> 45

```

</details>

<details><summary>data/warehouse · 💣 destructive · 6 destroy</summary>

```diff
- google_bigquery_dataset.legacy_events
- google_bigquery_dataset.legacy_users
- google_storage_bucket.legacy_exports
- google_storage_bucket.legacy_imports
- google_pubsub_topic.legacy_stream
- google_pubsub_subscription.legacy_sub
```

</details>

<details><summary>networking/shared-vpc · 💣 destructive · 5 change, 2 replace</summary>

```diff
# google_compute_subnetwork.s0 will be updated in-place
  + team: platform
~ retention_days: 23 -> 46

# google_compute_subnetwork.s1 will be updated in-place
  + team: platform
~ retention_days: 24 -> 47

# google_compute_subnetwork.s2 will be updated in-place
  + team: platform
~ retention_days: 25 -> 48

# google_compute_firewall.allow_internal will be updated in-place
  + team: platform
~ retention_days: 26 -> 49

# google_compute_firewall.allow_health will be updated in-place
  + team: platform
~ retention_days: 27 -> 50

# google_compute_instance.bastion will be replaced
~ machine_type: "e2-small" -> "e2-medium"

# google_compute_address.nat will be replaced
~ address_type: "INTERNAL" -> "EXTERNAL"

```

</details>

<details><summary>observability/grafana · ✅ safe · 5 add, 4 change</summary>

```diff
+ helm_release.grafana
+ helm_release.loki
+ kubernetes_namespace.observability
+ kubernetes_service_account.grafana
+ kubernetes_secret.grafana_admin
# kubernetes_config_map.dashboards will be updated in-place
  ~ data · text · 120 lines · 240 changed (hidden to fit size limit)

# google_storage_bucket.grafana_state will be updated in-place
  + team: platform
~ retention_days: 28 -> 51

# google_storage_bucket.loki_chunks will be updated in-place
  + team: platform
~ retention_days: 29 -> 52

# google_storage_bucket.loki_ruler will be updated in-place
  + team: platform
~ retention_days: 30 -> 53

```

</details>

<details><summary>security/secrets · 🔐 iam · 9 change</summary>

```diff
# google_secret_manager_secret_version.api_key will be updated in-place
~ secret_data: (sensitive value)

# google_secret_manager_secret_version.tls_cert will be updated in-place
~ secret_data: (sensitive value)

# google_secret_manager_secret_version.oauth_secret will be updated in-place
~ secret_data: (sensitive value)

# google_secret_manager_secret_version.signing_key will be updated in-place
~ secret_data: (sensitive value)

# google_project_iam_member.secret_accessors will be updated in-place
~ role: "roles/viewer" -> "roles/editor"

# google_project_iam_member.secret_admins will be updated in-place
~ role: "roles/viewer" -> "roles/editor"

# google_storage_bucket.audit_logs will be updated in-place
  + team: platform
~ retention_days: 31 -> 54

# google_storage_bucket.backups will be updated in-place
  + team: platform
~ retention_days: 32 -> 55

# google_storage_bucket.archive will be updated in-place
  + team: platform
~ retention_days: 33 -> 56

```

</details>

<!-- tfstackplan:state-ops -->
### Terraform plan — state ops & structured diffs  (2 stacks changed)

| Stack | Change | Class |
| --- | ---: | --- |
| infra/migrations · 1 import, 2 move, 1 forget | 2 | ✅ safe |
| platform/policies | 4 | 🔐 iam |

<details><summary>infra/migrations · ✅ safe · 2 change, 1 import, 2 move, 1 forget</summary>

> <details open><summary>↪ google_storage_bucket.assets · moved from google_storage_bucket.legacy_assets</summary>
>
> ```diff
> (address change only)
> ```
>
> </details>
> <details open><summary>↪ google_storage_bucket.state · moved from module.old.google_storage_bucket.state, 1 changed</summary>
>
> ```diff
> ~ retention_days = 7 → 30
> ```
>
> </details>
> <details open><summary>⤓ google_project.host · imported (id="my-host-project")</summary>
>
> ```diff
> (import only)
> ```
>
> </details>
> <details open><summary>⊘ aws_s3_bucket.legacy · forgotten · 2 attrs</summary>
>
> ```diff
> ⊘ name   = "legacy"
> ⊘ region = "us-east-1"
> ```
>
> </details>
> <details open><summary>~ google_compute_firewall.web · 2 changed</summary>
>
> ```diff
> ~ allow[0].ports[0] = "80" → "443"
> + source_ranges[1]  = "192.168.0.0/16"
> ```
>
> </details>

</details>

<details><summary>platform/policies · 🔐 iam · 4 change</summary>

> <details open><summary>~ aws_iam_policy.small · 1 changed</summary>
>
> ```diff
> ~ policy.Statement[0].Resource = "arn:aws:s3:::bucket-old-00/*" → "arn:aws:s3:::bucket-new-00/*"
> ```
>
> </details>
> <details><summary>~ aws_iam_policy.big · 1 changed</summary>
>
> ```diff
> ~ policy:
>   ~ Statement[0].Resource: arn:aws:s3:::bucket-old-00/* -> arn:aws:s3:::bucket-new-00/*
>   ~ Statement[10].Resource: arn:aws:s3:::bucket-old-10/* -> arn:aws:s3:::bucket-new-10/*
>   ~ Statement[11].Resource: arn:aws:s3:::bucket-old-11/* -> arn:aws:s3:::bucket-new-11/*
>   ~ Statement[1].Resource: arn:aws:s3:::bucket-old-01/* -> arn:aws:s3:::bucket-new-01/*
>   ~ Statement[2].Resource: arn:aws:s3:::bucket-old-02/* -> arn:aws:s3:::bucket-new-02/*
>   ~ Statement[3].Resource: arn:aws:s3:::bucket-old-03/* -> arn:aws:s3:::bucket-new-03/*
>   ~ Statement[4].Resource: arn:aws:s3:::bucket-old-04/* -> arn:aws:s3:::bucket-new-04/*
>   ~ Statement[5].Resource: arn:aws:s3:::bucket-old-05/* -> arn:aws:s3:::bucket-new-05/*
>   ~ Statement[6].Resource: arn:aws:s3:::bucket-old-06/* -> arn:aws:s3:::bucket-new-06/*
>   ~ Statement[7].Resource: arn:aws:s3:::bucket-old-07/* -> arn:aws:s3:::bucket-new-07/*
>   ~ Statement[8].Resource: arn:aws:s3:::bucket-old-08/* -> arn:aws:s3:::bucket-new-08/*
>   ~ Statement[9].Resource: arn:aws:s3:::bucket-old-09/* -> arn:aws:s3:::bucket-new-09/*
> ```
>
> </details>
> <details open><summary>~ kubernetes_manifest.app · 1 changed</summary>
>
> ```diff
> ~ manifest.spec.replicas                          = 2 → 4
> ~ manifest.spec.template.spec.containers[0].image = "app:1.4" → "app:1.5"
> ```
>
> </details>
> <details><summary>~ kubernetes_manifest.platform · 1 changed</summary>
>
> ```diff
> ~ manifest:
>   ~ spec.replicas: 2 -> 4
>   ~ spec.template.spec.containers[0].env[0].value: old -> new
>   ~ spec.template.spec.containers[0].env[1].value: old -> new
>   ~ spec.template.spec.containers[0].env[2].value: old -> new
>   ~ spec.template.spec.containers[0].env[3].value: old -> new
>   ~ spec.template.spec.containers[0].env[4].value: old -> new
>   ~ spec.template.spec.containers[0].env[5].value: old -> new
>   ~ spec.template.spec.containers[0].env[6].value: old -> new
>   ~ spec.template.spec.containers[0].env[7].value: old -> new
>   ~ spec.template.spec.containers[0].image: app:1.4 -> app:1.5
> ```
>
> </details>

</details>

<!-- tfstackplan:state-ops -->
### Terraform plan — state ops & structured diffs  (2 stacks changed)

| Stack | Change | Categories |
| --- | ---: | --- |
| infra/migrations · 1 import, 3 move, 1 forget | 2 | ✅ safe |
| platform/policies | 4 | 🔐 iam |

<details><summary>📁&nbsp;<b>infra/migrations</b> · ✅ safe · 2 change, 1 import, 3 move, 1 forget</summary>

>
> <details open><summary>↪️&nbsp;google_storage_bucket.assets<br>&nbsp;&nbsp;&nbsp;&nbsp;moved from google_storage_bucket.legacy_assets</summary>
>
> ```diff
> (address change only)
> ```
>
> </details>
>
> <details open><summary>↪️&nbsp;google_storage_bucket.state<br>&nbsp;&nbsp;&nbsp;&nbsp;moved from module.old.google_storage_bucket.state, 1 changed</summary>
>
> ```diff
> ~ retention_days = 7 → 30
> ```
>
> </details>
>
> <details open><summary>↪️&nbsp;google_project_iam_member.viewers<br>&nbsp;&nbsp;&nbsp;&nbsp;moved from google_project_iam_member.legacy_viewers</summary>
>
> ```diff
> (address change only)
> ```
>
> </details>
>
> <details open><summary>📥&nbsp;google_project.host<br>&nbsp;&nbsp;&nbsp;&nbsp;imported<br>&nbsp;&nbsp;&nbsp;&nbsp;<sub>id=<code>my-host-project</code></sub></summary>
>
> ```diff
> (import only)
> ```
>
> </details>
>
> <details open><summary>⏏️&nbsp;aws_s3_bucket.legacy<br>&nbsp;&nbsp;&nbsp;&nbsp;forgotten · 2 attrs</summary>
>
> ```diff
> ⊘ name   = "legacy"
> ⊘ region = "us-east-1"
> ```
>
> </details>
>
> <details open><summary>〰️&nbsp;google_compute_firewall.web<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ allow (yaml):
>  - ports:
> -    - "80"
> +    - "443"
>      - "8080"
>    protocol: tcp
> ~ source_ranges (yaml):
>  - 10.0.0.0/8
> +- 192.168.0.0/16
>  
> ```
>
> </details>

</details>

<details><summary>📁&nbsp;<b>platform/policies</b> · 🔐 iam · 4 change</summary>

>
> <details open><summary>〰️&nbsp;aws_iam_policy.small<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ policy (json):
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-00/*",
> +      "Resource": "arn:aws:s3:::bucket-new-00/*",
>        "Sid": "Stmt00"
>      },
> ```
>
> </details>
>
> <details><summary>〰️&nbsp;aws_iam_policy.big<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ policy (json):
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-00/*",
> +      "Resource": "arn:aws:s3:::bucket-new-00/*",
>        "Sid": "Stmt00"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-01/*",
> +      "Resource": "arn:aws:s3:::bucket-new-01/*",
>        "Sid": "Stmt01"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-02/*",
> +      "Resource": "arn:aws:s3:::bucket-new-02/*",
>        "Sid": "Stmt02"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-03/*",
> +      "Resource": "arn:aws:s3:::bucket-new-03/*",
>        "Sid": "Stmt03"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-04/*",
> +      "Resource": "arn:aws:s3:::bucket-new-04/*",
>        "Sid": "Stmt04"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-05/*",
> +      "Resource": "arn:aws:s3:::bucket-new-05/*",
>        "Sid": "Stmt05"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-06/*",
> +      "Resource": "arn:aws:s3:::bucket-new-06/*",
>        "Sid": "Stmt06"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-07/*",
> +      "Resource": "arn:aws:s3:::bucket-new-07/*",
>        "Sid": "Stmt07"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-08/*",
> +      "Resource": "arn:aws:s3:::bucket-new-08/*",
>        "Sid": "Stmt08"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-09/*",
> +      "Resource": "arn:aws:s3:::bucket-new-09/*",
>        "Sid": "Stmt09"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-10/*",
> +      "Resource": "arn:aws:s3:::bucket-new-10/*",
>        "Sid": "Stmt10"
>      },
> ⋮
>        ],
>        "Effect": "Allow",
> -      "Resource": "arn:aws:s3:::bucket-old-11/*",
> +      "Resource": "arn:aws:s3:::bucket-new-11/*",
>        "Sid": "Stmt11"
>      }
> ```
>
> </details>
>
> <details><summary>〰️&nbsp;kubernetes_manifest.app<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ manifest (yaml):
>  kind: Deployment
>  spec:
> -    replicas: 2
> +    replicas: 4
>      template:
>          spec:
> ⋮
>                      - name: VAR_7
>                        value: old
> -                  image: app:1.4
> +                  image: app:1.5
>                    name: app
>  
> ```
>
> </details>
>
> <details><summary>〰️&nbsp;kubernetes_manifest.platform<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ manifest (yaml):
>  kind: Deployment
>  spec:
> -    replicas: 2
> +    replicas: 4
>      template:
>          spec:
> ⋮
>                  - env:
>                      - name: VAR_0
> -                      value: old
> +                      value: new
>                      - name: VAR_1
> -                      value: old
> +                      value: new
>                      - name: VAR_2
> -                      value: old
> +                      value: new
>                      - name: VAR_3
> -                      value: old
> +                      value: new
>                      - name: VAR_4
> -                      value: old
> +                      value: new
>                      - name: VAR_5
> -                      value: old
> +                      value: new
>                      - name: VAR_6
> -                      value: old
> +                      value: new
>                      - name: VAR_7
> -                      value: old
> -                  image: app:1.4
> +                      value: new
> +                  image: app:1.5
>                    name: app
>  
> ```
>
> </details>

</details>

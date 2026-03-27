# Burst Family — Master Architecture

## Overview

The burst family is a set of language-native cloud bursting libraries that share a common
AWS backend. Each library provides idiomatic parallel map semantics in its host language,
transparently offloading work to AWS ECS workers.

**Family members:**

| Package | Language | Replaces | Distribution |
|---|---|---|---|
| `staRburst` | R | `future_map()` / `parLapply()` | CRAN |
| `adder` | Python | `ProcessPoolExecutor.map()` | PyPI |
| `Fatou.jl` | Julia | `pmap()` / `@distributed` | Julia General Registry |
| `stet` | TypeScript | `Promise.all()` / worker_threads | npm |
| `aji` | Go | `errgroup` / goroutine pool | pkg.go.dev |

**Shared infrastructure:**

| Repo | Purpose |
|---|---|
| `scttfrdmn/burst-core` | Go CLI, AWS provisioning, worker protocol |
| `burst-core.dev` | Family landing page and documentation |

---

## Design Principles

1. **One primitive** — every library exposes a `map()` function. No new concepts required.
2. **Drop-in substitution** — replacing the local parallel primitive with the cloud version
   requires changing one line of code.
3. **Zero infrastructure ownership** — the user never manages EC2 instances, ECS clusters,
   or Docker images directly.
4. **Transparent cost** — every run reports estimated and actual cost before and after.
5. **Language-native** — each library uses the idiomatic tools of its language for
   environment capture, serialization, and error handling.
6. **Shared backend** — all five libraries use the same AWS resource layout, S3 key schema,
   session model, and worker protocol. Differences are only at the language boundary.

---

## AWS Resource Layout

All resources are created by `burst-core setup` and shared across all language libraries
in the same AWS account/region.

```
burst-{region}/                          # S3 bucket (e.g. burst-us-east-1)
├── sessions/
│   └── {session-id}/
│       ├── manifest.json                # Session metadata
│       ├── tasks/
│       │   ├── {task-id}.task           # Serialized task input
│       │   └── {task-id}.result         # Serialized task output
│       └── status/
│           └── {task-id}.status         # "pending" | "running" | "done" | "failed"
├── images/
│   └── {env-hash}/
│       └── Dockerfile                   # Cached Dockerfile for env hash
└── artifacts/
    └── {lang}/
        └── {version}/
            └── worker                   # Worker binary (aji only)

ECR repository: burst-workers-{lang}     # One per language
ECS cluster: burst-cluster
IAM roles:
  burst-execution-role                   # ECS task execution (ECR pull, CloudWatch)
  burst-task-role                        # Task S3 access
VPC: default VPC, public subnets         # Configurable
```

### IAM Permissions Required by Caller

The AWS identity running `burst-core setup` needs:

- `iam:CreateRole`, `iam:AttachRolePolicy`, `iam:PassRole`
- `ecr:CreateRepository`, `ecr:GetAuthorizationToken`
- `ecs:CreateCluster`, `ecs:RegisterTaskDefinition`
- `s3:CreateBucket`, `s3:PutBucketPolicy`
- `ec2:DescribeVpcs`, `ec2:DescribeSubnets`, `ec2:DescribeSecurityGroups`
- `servicequotas:GetServiceQuota`

---

## Worker Lifecycle

Every language library follows this identical seven-step lifecycle:

### Step 1 — Environment Snapshot

Capture the caller's installed packages/dependencies and produce a deterministic hash.

| Language | Tool | Output | Hash input |
|---|---|---|---|
| R | renv | `renv.lock` | lock file SHA256 |
| Python | importlib.metadata | `requirements.txt` | sorted pkg==ver SHA256 |
| Julia | Pkg.jl | `Manifest.toml` | manifest SHA256 |
| TypeScript | esbuild bundle | `worker.bundle.js` | bundle SHA256 |
| Go | go build | `worker` binary | binary SHA256 |

### Step 2 — Container Build

If ECR already has an image tagged with the env hash, skip build. Otherwise:

1. Write `Dockerfile` to `s3://burst-{region}/images/{env-hash}/Dockerfile`
2. Build image locally using Docker daemon
3. Tag as `{account}.dkr.ecr.{region}.amazonaws.com/burst-workers-{lang}:{env-hash}`
4. Push to ECR

TypeScript and Go use lighter base images:

| Language | Base image | Approx size |
|---|---|---|
| R | `rocker/r-ver:{version}` | 800MB |
| Python | `python:{version}-slim` | 150MB |
| Julia | `julia:{version}` | 500MB |
| TypeScript | `node:{version}-slim` | 200MB |
| Go | `scratch` | 10MB |

### Step 3 — Session Initialization

1. Generate `session-id`: `{lang}-{timestamp}-{random-8}` e.g. `py-20260315-a3f7b2c1`
2. Write `s3://burst-{region}/sessions/{session-id}/manifest.json`:

```json
{
  "session_id": "py-20260315-a3f7b2c1",
  "language": "python",
  "library_version": "0.1.0",
  "env_hash": "sha256:abc123...",
  "created_at": "2026-03-15T10:00:00Z",
  "status": "initializing",
  "workers_requested": 50,
  "workers_actual": 50,
  "cpu": 4,
  "memory_gb": 8,
  "backend": "fargate",
  "region": "us-east-1",
  "cost_estimate_per_hour": 2.80,
  "task_count": 1000,
  "chunk_count": 50
}
```

### Step 4 — Task Decomposition

Split input data into `chunk_count` chunks where `chunk_count == workers_actual`.

- Chunks are as equal in size as possible (last chunk absorbs remainder)
- Each chunk produces one task file
- Task files are written to S3 before any workers launch

Task file naming: `s3://burst-{region}/sessions/{session-id}/tasks/{task-id}.task`
where `task-id` is zero-padded: `task-0000`, `task-0001`, etc.

### Step 5 — Worker Launch

Register an ECS task definition for this session:

```json
{
  "family": "burst-{session-id}",
  "taskRoleArn": "arn:aws:iam::{account}:role/burst-task-role",
  "executionRoleArn": "arn:aws:iam::{account}:role/burst-execution-role",
  "networkMode": "awsvpc",
  "requiresCompatibilities": ["FARGATE"],
  "cpu": "{cpu * 1024}",
  "memory": "{memory_gb * 1024}",
  "containerDefinitions": [{
    "name": "burst-worker",
    "image": "{ecr-uri}:{env-hash}",
    "environment": [
      {"name": "BURST_SESSION_ID", "value": "{session-id}"},
      {"name": "BURST_TASK_ID", "value": "{task-id}"},
      {"name": "BURST_S3_BUCKET", "value": "burst-{region}"},
      {"name": "BURST_REGION", "value": "{region}"},
      {"name": "BURST_LANG", "value": "{lang}"}
    ]
  }]
}
```

Launch `workers_actual` ECS tasks using `RunTask`. If quota limits are hit, wave-execute:
launch up to quota, wait for completion, launch next wave.

### Step 6 — Worker Execution

Each worker:

1. Reads env vars to determine session ID and task ID
2. Downloads `s3://burst-{region}/sessions/{session-id}/tasks/{task-id}.task`
3. Deserializes task data using language-native format
4. Writes `{task-id}.status` = `"running"`
5. Executes the function on the chunk items
6. Serializes results
7. Writes `s3://burst-{region}/sessions/{session-id}/tasks/{task-id}.result`
8. Writes `{task-id}.status` = `"done"` (or `"failed"` with error payload)

Workers are stateless. They do not communicate with each other.

### Step 7 — Collection

Client polls S3 status files every 2 seconds. When all status files are `"done"`:

1. Download all result files in parallel
2. Deserialize each result
3. Reassemble in original order (task-0000 first, etc.)
4. Flatten chunks back into a single result list
5. Update manifest status to `"complete"`, write final cost
6. Delete task files and status files (keep manifest for 7 days)
7. Deregister ECS task definition

---

## Detached Session Model

All five libraries implement an identical detached session model.

A detached session completes steps 1–5 synchronously, then returns a `session_id` string.
The client process can exit. Workers continue running and write results to S3.

To reattach:

```
session = attach(session_id)  # language-specific call
status = session.status()     # returns SessionStatus struct (see below)
results = session.collect()   # blocks until complete, returns results
session.cleanup()             # deletes S3 artifacts
```

### SessionStatus

All languages expose an equivalent struct/record/object:

```
SessionStatus {
  session_id:         string
  status:             "initializing" | "running" | "complete" | "failed" | "partial"
  tasks_total:        int
  tasks_complete:     int
  tasks_failed:       int
  workers_active:     int
  elapsed_seconds:    float
  cost_actual:        float      # USD, 0 if not yet complete
  cost_estimate:      float      # USD/hour
}
```

---

## Cost Reporting Format

All five libraries emit identical console output for cost events. This is non-negotiable
for consistency — implement exactly:

```
🚀 Starting burst cluster with {n} workers
💰 Estimated cost: ~${rate}/hour
📊 Processing {total} items with {n} workers
📦 Created {chunks} chunks (avg {avg} items per chunk)
🚀 Submitting tasks...
✓ Submitted {n} tasks
⏳ Progress: {done}/{total} tasks ({elapsed} elapsed)

✓ Completed in {elapsed}
💰 Actual cost: ${cost}
```

Warning variants (emit before progress line):
```
⚠ Requested {n} workers ({vcpu} vCPUs) but quota allows {actual} workers ({actual_vcpu} vCPUs)
⚠ Using {actual} workers instead. Request quota increase: https://console.aws.amazon.com/servicequotas/
⚠ Estimated cost exceeds alert threshold of ${threshold}
```

---

## Error Type Hierarchy

All languages implement equivalent structured error types. Name them idiomatically per
language but ensure the same fields exist.

### BurstPartialError
Some tasks completed, some failed. Always includes partial results.

Fields:
- `results` — list of successful results (in original order, nil/null for failed positions)
- `errors` — list of per-task errors (nil/null for successful positions)
- `failed_count` — int
- `success_count` — int

### BurstQuotaError
AWS quota prevented launching requested workers.

Fields:
- `requested_workers` — int
- `actual_workers` — int
- `quota_name` — string
- `quota_value` — float

### BurstCostLimitError
Job hit the configured cost ceiling before completion.

Fields:
- `limit` — float (USD)
- `estimated_cost` — float (USD)
- `partial_results` — list (what completed before limit hit)

### BurstTimeoutError
Detached session exceeded maximum wait time on collect().

Fields:
- `session_id` — string
- `timeout_seconds` — int
- `status` — SessionStatus at time of timeout

### BurstSetupError
AWS resource provisioning failed.

Fields:
- `step` — string (which setup step failed)
- `cause` — string (AWS error message)
- `remediation` — string (human-readable fix suggestion)

---

## Configuration

All libraries read from the same config file: `~/.burst/config.json`

```json
{
  "region": "us-east-1",
  "s3_bucket": "burst-us-east-1",
  "ecs_cluster": "burst-cluster",
  "ecr_base_uri": "{account}.dkr.ecr.{region}.amazonaws.com",
  "execution_role_arn": "arn:aws:iam::{account}:role/burst-execution-role",
  "task_role_arn": "arn:aws:iam::{account}:role/burst-task-role",
  "default_cpu": 2,
  "default_memory_gb": 4,
  "default_workers": 10,
  "max_cost_per_job": null,
  "cost_alert_threshold": null,
  "backend": "fargate",
  "spot": false
}
```

All per-call options override config file values. Config file values override defaults.

---

## Session ID Format

`{lang-2char}-{yyyymmdd}-{random-8hex}`

Examples:
- `r_-20260315-a3f7b2c1`
- `py-20260315-b7e3f901`
- `jl-20260315-c4a91d23`
- `ts-20260315-d8b20e44`
- `go-20260315-e1c3f567`

The language prefix allows session IDs to be unambiguous across libraries.

---

## S3 Key Schema (Canonical)

```
burst-{region}/
  sessions/
    {session-id}/
      manifest.json
      tasks/
        {task-id}.task      # binary, language-specific serialization
        {task-id}.result    # binary, language-specific serialization
        {task-id}.status    # text: "pending"|"running"|"done"|"failed"
        {task-id}.error     # text: error message (only if status=failed)
  images/
    {lang}/
      {env-hash}/
        Dockerfile
        build.log
  artifacts/
    go/
      {version}/
        worker-linux-amd64  # aji worker binary
```

---

## burst-core CLI Interface

All language libraries shell out to `burst-core` for AWS operations. The binary must be
present in PATH or at `~/.burst/bin/burst-core`.

```
burst-core setup [--region us-east-1] [--profile default]
burst-core teardown [--force]
burst-core status
burst-core session list
burst-core session status {session-id}
burst-core session cleanup {session-id}
burst-core session cleanup --all --older-than 7d
burst-core image list
burst-core image prune --older-than 30d
burst-core quota check [--region us-east-1]
burst-core cost report [--days 30]
burst-core version
```

All commands output JSON when `--json` flag is passed.
All commands respect `--region`, `--profile`, `--bucket` overrides.
All commands return exit code 0 on success, non-zero on failure.
Exit code 2 = configuration error, 3 = AWS error, 4 = quota error.

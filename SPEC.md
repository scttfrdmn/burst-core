# burst-core — Implementation Spec

## Overview

`burst-core` is a Go CLI binary and Go library that provides shared AWS infrastructure
for the entire burst family. It is the only component that directly calls AWS APIs for
resource provisioning. All five language libraries depend on it.

This repo also serves as the Go module for `aji` via a sub-package (see aji/SPEC.md).

---

## Repository Structure

```
burst-core/
├── cmd/
│   └── burst-core/
│       └── main.go              # CLI entry point
├── internal/
│   ├── aws/
│   │   ├── ecs.go               # ECS task registration and launch
│   │   ├── ecr.go               # ECR repository management
│   │   ├── s3.go                # S3 bucket operations
│   │   ├── iam.go               # IAM role provisioning
│   │   ├── vpc.go               # VPC/subnet/security group lookup
│   │   └── quota.go             # Service quota checking
│   ├── config/
│   │   ├── config.go            # ~/.burst/config.json read/write
│   │   └── defaults.go          # Default values
│   ├── session/
│   │   ├── session.go           # Session lifecycle management
│   │   ├── manifest.go          # Manifest read/write
│   │   └── status.go            # Status polling
│   ├── cost/
│   │   └── cost.go              # Cost estimation and reporting
│   └── docker/
│       └── build.go             # Docker image build and push
├── pkg/
│   └── protocol/
│       ├── types.go             # Shared types (SessionStatus, ErrorTypes, etc.)
│       └── schema.go            # S3 key schema helpers
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

---

## Language and Tooling

- Go 1.22+
- AWS SDK for Go v2 (`github.com/aws/aws-sdk-go-v2`)
- CLI framework: `github.com/spf13/cobra`
- Config: `github.com/spf13/viper`
- Output: `github.com/charmbracelet/lipgloss` for styled terminal output
- JSON: standard `encoding/json`

---

## `burst-core setup`

This is the most critical command. It must be idempotent — running it twice produces the
same result as running it once.

### Execution steps (in order):

**1. Validate AWS credentials**
- Attempt `sts:GetCallerIdentity`
- If fails: print clear error with instructions to run `aws configure`
- Extract account ID and region from identity

**2. Create S3 bucket**
- Bucket name: `burst-{region}` (e.g. `burst-us-east-1`)
- If bucket exists and is owned by this account: skip
- If bucket exists and is owned by another account: BurstSetupError
- Enable versioning: no
- Block all public access: yes
- Enable SSE-S3 encryption
- Add lifecycle rule: delete objects under `sessions/` prefix after 7 days
- Add lifecycle rule: delete objects under `images/` prefix after 30 days

**3. Create IAM roles**

Role 1: `burst-execution-role`
- Trust policy: `ecs-tasks.amazonaws.com`
- Managed policies to attach:
  - `arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy`
  - Inline policy for ECR pull on `burst-workers-*` repos

Role 2: `burst-task-role`
- Trust policy: `ecs-tasks.amazonaws.com`
- Inline policy:
  ```json
  {
    "Version": "2012-10-17",
    "Statement": [{
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::burst-{region}",
        "arn:aws:s3:::burst-{region}/*"
      ]
    }]
  }
  ```

**4. Create ECS cluster**
- Cluster name: `burst-cluster`
- Type: FARGATE (no EC2 capacity providers needed for default setup)
- Enable Container Insights: yes

**5. Check Fargate quotas**
- Check `L-3032A538` (Fargate On-Demand vCPU) via Service Quotas API
- Check `L-7F6USBRE` (Fargate Spot vCPU) if spot mode
- Store quota values in config
- Print current quota values to user

**6. Write config file**
- Write `~/.burst/config.json` with all provisioned resource ARNs and names
- Create `~/.burst/` directory if it doesn't exist
- Set permissions 0600 on config file

**7. Print summary**
```
✓ burst-core setup complete

  S3 bucket:      burst-us-east-1
  ECS cluster:    burst-cluster
  Execution role: arn:aws:iam::123456789:role/burst-execution-role
  Task role:      arn:aws:iam::123456789:role/burst-task-role
  Fargate quota:  256 vCPUs on-demand

Run `burst-core status` to verify configuration.
```

---

## `burst-core teardown`

Removes all AWS resources created by setup. Requires `--force` flag.

Steps:
1. Delete all ECR repositories matching `burst-workers-*`
2. Deregister all ECS task definitions matching `burst-*`
3. Delete ECS cluster `burst-cluster`
4. Detach and delete IAM policies, delete roles `burst-execution-role` and `burst-task-role`
5. Empty and delete S3 bucket `burst-{region}`
6. Delete `~/.burst/config.json`

Print each step as it completes. If any step fails, continue and report all failures at end.

---

## `burst-core status`

Reads `~/.burst/config.json` and verifies each resource exists and is in expected state.

Output:
```
burst-core status

  Config file:    ~/.burst/config.json ✓
  AWS identity:   arn:aws:iam::123456789:user/scott ✓
  S3 bucket:      burst-us-east-1 ✓
  ECS cluster:    burst-cluster (ACTIVE) ✓
  Execution role: burst-execution-role ✓
  Task role:      burst-task-role ✓
  Fargate quota:  256 vCPUs available

  ECR repositories:
    burst-workers-r      3 images (newest: 2026-03-10)
    burst-workers-python 1 image  (newest: 2026-03-14)

  Active sessions: 0
```

---

## `burst-core session list`

Lists all sessions in S3 (reads all `sessions/*/manifest.json` objects).

```
SESSION ID                 LANG  STATUS    TASKS      COST      AGE
py-20260315-a3f7b2c1      py    complete  1000/1000  $0.29     2 hours
r_-20260314-b8e4f902      r     complete  500/500    $0.14     1 day
jl-20260313-c3d5e011      jl    failed    230/500    $0.08     2 days
```

---

## `burst-core session status {session-id}`

Reads and pretty-prints the manifest for a specific session.

---

## `burst-core session cleanup {session-id}`

Deletes all S3 objects under `sessions/{session-id}/`.
Requires session to be in `complete` or `failed` status unless `--force` is passed.

`burst-core session cleanup --all --older-than 7d`
Cleans up all sessions older than the specified duration.

---

## `burst-core quota check`

Checks all relevant Fargate quotas and prints current values vs limits.
Provides direct links to Service Quotas console for each quota that is low.

---

## `burst-core cost report [--days 30]`

Queries AWS Cost Explorer for costs tagged with `burst-session-id`.
Groups by session ID, language, date.
Prints table and total.

Note: requires `ce:GetCostAndUsage` permission. Print helpful message if missing.

---

## `pkg/protocol` — Shared Types

This package is imported by `aji` and can be imported by any Go code that needs to
interact with burst sessions.

```go
package protocol

// SessionStatus is the canonical status structure for all burst sessions.
// All language libraries expose an equivalent structure.
type SessionStatus struct {
    SessionID       string    `json:"session_id"`
    Language        string    `json:"language"`
    Status          string    `json:"status"` // initializing|running|complete|failed|partial
    TasksTotal      int       `json:"tasks_total"`
    TasksComplete   int       `json:"tasks_complete"`
    TasksFailed     int       `json:"tasks_failed"`
    WorkersActive   int       `json:"workers_active"`
    ElapsedSeconds  float64   `json:"elapsed_seconds"`
    CostActual      float64   `json:"cost_actual"`
    CostEstimate    float64   `json:"cost_estimate_per_hour"`
    CreatedAt       time.Time `json:"created_at"`
    UpdatedAt       time.Time `json:"updated_at"`
}

// Manifest is the full session manifest stored in S3.
type Manifest struct {
    SessionStatus
    EnvHash          string `json:"env_hash"`
    LibraryVersion   string `json:"library_version"`
    WorkersRequested int    `json:"workers_requested"`
    WorkersActual    int    `json:"workers_actual"`
    CPU              int    `json:"cpu"`
    MemoryGB         int    `json:"memory_gb"`
    Backend          string `json:"backend"` // fargate|ec2
    Spot             bool   `json:"spot"`
    Region           string `json:"region"`
    ChunkCount       int    `json:"chunk_count"`
    TaskCount        int    `json:"task_count"`
}

// S3Keys returns the canonical S3 key paths for a session.
func S3Keys(bucket, sessionID string) SessionKeys

type SessionKeys struct {
    Manifest  string // sessions/{id}/manifest.json
    TasksDir  string // sessions/{id}/tasks/
    TaskKey   func(taskID string) string   // sessions/{id}/tasks/{taskID}.task
    ResultKey func(taskID string) string   // sessions/{id}/tasks/{taskID}.result
    StatusKey func(taskID string) string   // sessions/{id}/tasks/{taskID}.status
    ErrorKey  func(taskID string) string   // sessions/{id}/tasks/{taskID}.error
}
```

---

## Config Package

```go
package config

type Config struct {
    Region             string  `json:"region"`
    S3Bucket           string  `json:"s3_bucket"`
    ECSCluster         string  `json:"ecs_cluster"`
    ECRBaseURI         string  `json:"ecr_base_uri"`
    ExecutionRoleARN   string  `json:"execution_role_arn"`
    TaskRoleARN        string  `json:"task_role_arn"`
    DefaultCPU         int     `json:"default_cpu"`
    DefaultMemoryGB    int     `json:"default_memory_gb"`
    DefaultWorkers     int     `json:"default_workers"`
    MaxCostPerJob      float64 `json:"max_cost_per_job"`      // 0 = no limit
    CostAlertThreshold float64 `json:"cost_alert_threshold"`  // 0 = no alert
    Backend            string  `json:"backend"`               // fargate|ec2
    Spot               bool    `json:"spot"`
    FargateQuotaVCPU   float64 `json:"fargate_quota_vcpu"`
}

func Load() (*Config, error)           // reads ~/.burst/config.json
func (c *Config) Save() error          // writes ~/.burst/config.json
func (c *Config) Validate() error      // checks all required fields present
```

---

## Docker Build Package

```go
package docker

// BuildAndPush builds a Docker image from a Dockerfile string and pushes to ECR.
// Returns the full ECR image URI.
// Skips build if ECR already has the env-hash tag.
func BuildAndPush(ctx context.Context, opts BuildOptions) (string, error)

type BuildOptions struct {
    Dockerfile  string   // Dockerfile content as string
    Lang        string   // "r", "python", "julia", "typescript", "go"
    EnvHash     string   // SHA256 of environment snapshot
    ECRBaseURI  string
    Region      string
    BuildArgs   map[string]string
}
```

Build output is streamed to stderr with a progress prefix:
```
  [docker] Step 1/8 : FROM python:3.11-slim
  [docker] Step 2/8 : COPY requirements.txt .
  ...
  [ecr]    Pushing layer sha256:abc123...
  ✓ Image pushed: {account}.dkr.ecr.us-east-1.amazonaws.com/burst-workers-python:sha256:abc123
```

---

## Release and Distribution

`burst-core` must be distributed as a pre-compiled binary for:
- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`
- `windows/amd64`

GitHub Actions workflow releases binaries on tag push.

Language libraries that depend on `burst-core` should:
1. Check for `burst-core` in PATH
2. Check for `~/.burst/bin/burst-core`
3. If not found, print installation instructions pointing to `burst-core.dev/install`
4. Provide a convenience installer function/command that downloads the correct binary
   for the current platform from the GitHub releases page

The install script at `burst-core.dev/install`:
```bash
curl -fsSL https://burst-core.dev/install | sh
```
Downloads the correct binary to `~/.burst/bin/burst-core` and adds to PATH.

---

## Testing

- Unit tests for all `internal/` packages using standard `testing`
- Integration tests under `integration/` that require real AWS credentials
- Integration tests are skipped unless `BURST_INTEGRATION_TEST=1` env var is set
- Use `testcontainers-go` for Docker-dependent tests
- Minimum 80% coverage on `internal/` packages
- `make test` runs unit tests only
- `make test-integration` runs all tests

---

## Makefile Targets

```makefile
build:          # Build binary for current platform
build-all:      # Cross-compile for all platforms
test:           # Run unit tests
test-integration: # Run integration tests (requires AWS credentials)
lint:           # Run golangci-lint
release:        # Tag and push (triggers GitHub Actions)
install:        # Install to ~/.burst/bin/burst-core
```

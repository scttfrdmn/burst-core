# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-03-26

### Added
- `burst-core session list/status/cleanup`: session management CLI subcommands with `--force`, `--all`, `--older-than` flags
- `burst-core image list/prune`: ECR image management CLI subcommands
- `burst-core quota check`: Fargate vCPU quota display with Service Quotas console link
- `burst-core cost report [--days N]`: AWS Cost Explorer daily cost breakdown; graceful `ErrCEPermissionDenied` handling
- `internal/session`: `ReadManifest`, `WriteManifest`, `ListSessions` (sorted newest-first), `DeleteSession`; accepts a `S3Client` interface for testability
- `internal/docker`: `BuildAndPush` — idempotent Docker build + ECR push with `[docker]`/`[ecr]` prefixed streaming output; `ECRClient` interface for testability
- `internal/aws/ce`: `CostExplorerClient.GetBurstCosts` with `ErrCEPermissionDenied` sentinel
- `internal/aws`: `ECRClient.ListImages`, `ECRClient.DeleteImages`, `ECRClient.ImageDetail`

## [0.2.0] - 2026-03-26

### Added
- `burst-core setup`: idempotent 7-step AWS provisioning (S3, IAM, ECS, Fargate quota check, config write)
- `burst-core teardown --force`: full resource cleanup with per-step error reporting (ECR, ECS task definitions, ECS cluster, IAM roles, S3 bucket, config file)
- `burst-core status`: verify all provisioned resources with formatted output; `--json` flag for structured output
- `burst-core version`: print binary version (injected at build time via ldflags)
- Global flags: `--region`, `--profile`, `--json`, `--bucket`
- `internal/aws/sts`: `STSClient.GetCallerIdentity` for credential validation and account ID extraction
- `internal/aws`: `ECRClient.DeleteRepository`, `ECRClient.ListBurstRepositories`, `ECRClient.ImageCount`
- `internal/aws`: `ECSClient.DeleteCluster`, `ECSClient.ClusterStatus`, `ECSClient.ListTaskDefinitionFamilies`, `ECSClient.DeregisterAllRevisions`
- `internal/aws`: `IAMClient.RoleExists`, `IAMClient.DeleteRole` (detaches all policies before deletion)
- `internal/aws`: `S3Client.EmptyAndDeleteBucket` (paginated deletion then bucket removal)
- `internal/config`: exported `ConfigPath()` function

## [0.1.0] - 2026-03-26

### Added
- `pkg/protocol`: canonical `SessionStatus` and `Manifest` structs shared by all burst family libraries
- `pkg/protocol`: 5 error types — `BurstPartialError`, `BurstQuotaError`, `BurstCostLimitError`, `BurstTimeoutError`, `BurstSetupError`
- `pkg/protocol`: S3 key schema helpers (`S3Keys`, `SessionKeys`), session ID generation, task ID formatting
- `internal/config`: `Config` struct with `Load`, `Save` (0600 permissions), `Validate`; sensible defaults
- `internal/aws`: `S3Client` — idempotent bucket creation with SSE-S3 encryption, public access block, lifecycle rules
- `internal/aws`: `IAMClient` — idempotent `burst-execution-role` and `burst-task-role` provisioning
- `internal/aws`: `ECRClient` — idempotent repository creation, image existence check, auth token
- `internal/aws`: `ECSClient` — cluster/task-definition/run-task/describe-tasks/stop-task operations
- `internal/aws`: `VPCClient` — default VPC subnet and security group lookup
- `internal/aws`: `QuotaClient` — Fargate on-demand and Spot vCPU quota lookup
- `internal/cost`: `EstimateCost`, `EstimateCostPerHour`, and all canonical display format functions matching ARCHITECTURE.md exactly

# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

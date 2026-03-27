# Fatou.jl — Julia Cloud Bursting Library

## Overview

`Fatou.jl` is a Julia package that provides cloud bursting to AWS for parallel workloads.
It integrates with Julia's standard distributed computing infrastructure via
`ClusterManagers.jl`, making it compatible with the full Julia parallel ecosystem
including `pmap()`, `@distributed`, `Distributed.remotecall()`, and `Dagger.jl`.

The name references the Fatou set in complex dynamics — the complement of the Julia set,
named after Pierre Fatou. Latent potential, activated on demand.

Read ARCHITECTURE.md before implementing. All session management, S3 key schema,
cost reporting format, error types, and worker lifecycle must conform to that spec.

---

## Repository Structure

```
Fatou.jl/
├── src/
│   ├── Fatou.jl                 # Module definition and exports
│   ├── manager.jl               # CloudManager <: ClusterManager
│   ├── session.jl               # Session lifecycle
│   ├── env.jl                   # Environment snapshot and container build
│   ├── serialize.jl             # JLD2/HDF5 serialization
│   ├── worker.jl                # Worker entrypoint (runs in ECS container)
│   ├── config.jl                # Config file (~/.burst/config.json)
│   ├── errors.jl                # Exception types
│   ├── cost.jl                  # Cost estimation and display
│   └── macro.jl                 # @cloud macro
├── test/
│   ├── runtests.jl
│   ├── unit/
│   └── integration/             # Requires BURST_INTEGRATION_TEST=1
├── Project.toml
├── Manifest.toml
├── README.md
└── Dockerfile.worker            # Template Dockerfile for workers
```

---

## Language and Tooling

- Julia 1.10+
- AWS SDK: `AWS.jl` >= 1.88
- Serialization: `JLD2.jl` for Julia-native types, `Serialization` stdlib for fallback
- Progress: `ProgressMeter.jl`
- JSON: `JSON3.jl`
- ClusterManagers integration: `ClusterManagers.jl` >= 0.4
- Package compilation: `PackageCompiler.jl` >= 2.1

---

## The Cold Start Problem — Critical Implementation Detail

Julia's JIT compilation means loading packages on a fresh worker is slow (30–90 seconds
for heavy scientific packages). This is unacceptable for interactive use.

**Solution: PackageCompiler.jl sysimage baked into the Docker image.**

During container build (Step 2 of the worker lifecycle), after installing packages:

```dockerfile
# In the generated Dockerfile:
RUN julia --project=/app -e '
    using PackageCompiler
    create_sysimage(
        [packages...],          # all packages in Project.toml
        sysimage_path="/app/burst.so",
        precompile_execution_file="/app/precompile.jl"
    )
'
```

Where `precompile.jl` imports each package and calls representative functions
to trigger compilation of the hot paths.

Workers launch with:
```dockerfile
CMD ["julia", "--sysimage=/app/burst.so", "--project=/app", "/app/worker.jl"]
```

This adds 5–15 minutes to the *first* container build for a given environment hash.
Subsequent cold starts for workers using a cached image are ~2–3 seconds.

**This step is non-negotiable.** A Fatou.jl that doesn't solve cold starts is not useful.

Print a clear message during the first build:
```
📦 Building worker image with precompiled sysimage...
   This takes 5-15 minutes for the first environment.
   Subsequent runs with the same packages will be instant.
   Building... [spinner]
```

---

## ClusterManager Integration

`CloudManager` implements the `ClusterManager` abstract type from `ClusterManagers.jl`.

```julia
struct CloudManager <: ClusterManager
    session_id::String
    workers_requested::Int
    workers_actual::Int
    cpu::Int
    memory_gb::Int
    backend::Symbol          # :fargate or :ec2
    spot::Bool
    region::String
end

# Required ClusterManager interface:
function ClusterManagers.launch(
    manager::CloudManager,
    params::Dict,
    launched::Array,
    c::Condition
)
    # Launch ECS tasks
    # Each task runs worker.jl, which starts a Julia worker process
    # Worker connects back to client using TCP on the ECS task's private IP
    # Add WorkerConfig for each launched worker to `launched`
end

function ClusterManagers.manage(
    manager::CloudManager,
    id::Integer,
    config::WorkerConfig,
    op::Symbol
)
    # Handle :register, :interrupt, :finalize operations
end
```

Workers communicate with the client using Julia's built-in distributed protocol
(TCP sockets). Each ECS worker task:
1. Starts a Julia worker process
2. Establishes connection back to client's public IP on the port Julia negotiates
3. Registers itself as a Julia worker process

The client's public IP must be determined at session start and passed to workers
via ECS environment variables.

### `addcloudprocs()`

```julia
function addcloudprocs(
    n::Int;
    cpu::Int = 2,
    memory::String = "4GB",
    backend::Symbol = :fargate,
    spot::Bool = false,
    region::Union{String,Nothing} = nothing,
    timeout::Int = 300,    # seconds to wait for all workers to connect
) -> Vector{Int}           # returns worker process IDs
```

Returns Julia process IDs — exactly like `addprocs()`. After this call, all standard
Julia distributed primitives work on cloud workers:

```julia
# All of these work with no changes after addcloudprocs():
results = pmap(simulate, seeds)
results = fetch.([@spawnat w f(x) for (w,x) in zip(workers(), items)])
@sync @distributed for i in 1:n
    do_work(i)
end
```

---

## Convenience API

### `cloud_pmap()`

For users who don't want to manage process IDs:

```julia
function cloud_pmap(
    f::Function,
    items;
    workers::Int = 10,
    cpu::Int = 2,
    memory::String = "4GB",
    backend::Symbol = :fargate,
    spot::Bool = false,
    max_cost::Union{Float64,Nothing} = nothing,
    region::Union{String,Nothing} = nothing,
) -> Vector
```

Provisions workers, runs `pmap(f, items)` across them, shuts down workers, returns results.

### `@cloud` Macro

```julia
macro cloud(kwargs..., expr)
```

Usage:
```julia
results = @cloud workers=50 cpu=4 begin
    pmap(simulate, seeds)
end

# Also valid:
@cloud workers=100 begin
    @distributed (+) for i in 1:10_000
        expensive_computation(i)
    end
end
```

The macro:
1. Parses keyword arguments (workers, cpu, memory, backend, spot, max_cost)
2. Calls `addcloudprocs()` with those arguments
3. Evaluates the expression in the worker context
4. Calls `rmprocs()` to shut down cloud workers
5. Returns the expression result

Workers are always cleaned up even if the expression throws.

---

## Serialization

Use `JLD2.jl` for task data serialization. JLD2 handles the full Julia type system
including custom structs, arrays of structs, and most Julia-native types.

```julia
using JLD2

function serialize_task(fn::Function, items::Vector) :: Vector{UInt8}
    buf = IOBuffer()
    jldsave(buf; fn=fn, items=items)
    take!(buf)
end

function deserialize_task(data::Vector{UInt8}) :: Tuple{Function, Vector}
    buf = IOBuffer(data)
    d = load(buf)
    d["fn"], d["items"]
end

function serialize_result(result) :: Vector{UInt8}
    buf = IOBuffer()
    jldsave(buf; result=result)
    take!(buf)
end

function deserialize_result(data::Vector{UInt8})
    buf = IOBuffer(data)
    load(buf)["result"]
end
```

For large numerical arrays (>100MB), prefer HDF5.jl for better performance.
The choice is made automatically based on the detected type and size of items.

---

## Environment Snapshot (`env.jl`)

```julia
function capture_environment() :: Tuple{String, String}
    # Returns (manifest_content::String, env_hash::String)
    # Reads Project.toml and Manifest.toml from the active project
    # env_hash is SHA256 of Manifest.toml content
    # If no active project, uses Base.active_project()
end
```

Docker template (`Dockerfile.worker`):
```dockerfile
FROM julia:{julia_version}
WORKDIR /app
COPY Project.toml Manifest.toml ./
RUN julia --project=/app -e 'using Pkg; Pkg.instantiate()'
# PackageCompiler sysimage (see Cold Start section above)
RUN julia --project=/app -e '
    using PackageCompiler
    pkgs = [Symbol(k) for k in keys(TOML.parsefile("Project.toml")["deps"])]
    create_sysimage(pkgs, sysimage_path="/app/burst.so",
                    precompile_execution_file="/app/precompile.jl")
'
COPY worker.jl precompile.jl ./
ENV JULIA_NUM_THREADS=auto
CMD ["julia", "--sysimage=/app/burst.so", "--project=/app", "worker.jl"]
```

`{julia_version}` is the caller's exact Julia version (e.g. `1.10.2` → `1.10`).

Generate `precompile.jl` automatically by importing all packages in Project.toml:
```julia
# Generated precompile.jl
using Package1
using Package2
# ... one using statement per dependency
```

---

## Worker Entrypoint (`worker.jl`)

This runs inside the ECS container. Must work with only stdlib + user's packages.

```julia
#!/usr/bin/env julia
using Distributed
using AWS
using JLD2

const SESSION_ID = ENV["BURST_SESSION_ID"]
const TASK_ID    = ENV["BURST_TASK_ID"]
const BUCKET     = ENV["BURST_S3_BUCKET"]
const REGION     = ENV["BURST_REGION"]
const MODE       = get(ENV, "BURST_MODE", "task")  # "task" or "worker"

function main()
    if MODE == "worker"
        # Start as Julia worker process for addcloudprocs() mode
        start_worker(ENV["BURST_CLIENT_HOST"], parse(Int, ENV["BURST_CLIENT_PORT"]))
        return
    end

    # Task mode: download task, execute, upload result
    s3_client = AWS.global_aws_config(region=REGION)

    status_key = "sessions/$SESSION_ID/tasks/$TASK_ID.status"
    task_key   = "sessions/$SESSION_ID/tasks/$TASK_ID.task"
    result_key = "sessions/$SESSION_ID/tasks/$TASK_ID.result"
    error_key  = "sessions/$SESSION_ID/tasks/$TASK_ID.error"

    put_s3(s3_client, BUCKET, status_key, "running")

    try
        task_data = get_s3(s3_client, BUCKET, task_key)
        fn, items = deserialize_task(task_data)
        results = [fn(item) for item in items]
        put_s3(s3_client, BUCKET, result_key, serialize_result(results))
        put_s3(s3_client, BUCKET, status_key, "done")
    catch e
        put_s3(s3_client, BUCKET, error_key, sprint(showerror, e, catch_backtrace()))
        put_s3(s3_client, BUCKET, status_key, "failed")
        exit(1)
    end
end

main()
```

---

## Error Types (`errors.jl`)

```julia
struct BurstPartialError <: Exception
    results::Vector          # Union{T, Nothing} for each item
    errors::Vector           # Union{Exception, Nothing} for each item
    failed_count::Int
    success_count::Int
end

struct BurstQuotaError <: Exception
    requested_workers::Int
    actual_workers::Int
    quota_name::String
    quota_value::Float64
end

struct BurstCostLimitError <: Exception
    limit::Float64
    estimated_cost::Float64
    partial_results::Vector
end

struct BurstTimeoutError <: Exception
    session_id::String
    timeout_seconds::Int
    status  # SessionStatus
end

struct BurstSetupError <: Exception
    step::String
    cause::String
    remediation::String
end
```

---

## Detached Sessions

```julia
# Start detached
session = Fatou.session(workers=50, detached=true)
session_id = Fatou.submit!(session, items, fn)
# Julia process can exit — job continues

# Reattach
session = Fatou.attach(session_id)
status = Fatou.status(session)   # Returns SessionStatus
results = Fatou.collect!(session) # Blocks until complete
Fatou.cleanup!(session)
```

`SessionStatus` is a struct with fields matching ARCHITECTURE.md canonical schema.

---

## `Project.toml`

```toml
name = "Fatou"
uuid = "..." # generate with UUIDs.uuid4()
authors = ["Scott Friedman <scott@burst-core.dev>"]
version = "0.1.0"

[deps]
AWS = "fbe9abb3-538b-5e4e-ba9e-bc37a67bde72"
ClusterManagers = "34f1f09b-3a8b-5176-ab39-66d58a4d544e"
JLD2 = "033835bb-8acc-5ee8-8aae-3f567f8a3819"
JSON3 = "0f8b85d8-7e73-4b5f-9b8b-7dcf0e3ac576"
PackageCompiler = "9b87118b-4619-50d2-8e1e-99f35a4d4d9d"
ProgressMeter = "92933f4c-e287-5a05-a399-4b506db050ca"

[compat]
julia = "1.10"
```

---

## Testing

- Unit tests: mock AWS calls using a local S3-compatible server (MinIO in Docker)
- Integration tests: require `BURST_INTEGRATION_TEST=1`
- Test that `pmap()` works correctly after `addcloudprocs()` (integration)
- Test sysimage build and worker cold start time (integration)
- Test JLD2 serialization roundtrip for common types
- Test `@cloud` macro parses kwargs correctly and cleans up workers on error

---

## Registration in Julia General Registry

After initial release:
1. Tag v0.1.0 in the GitHub repo
2. Comment `@JuliaRegistrator register` on the tag commit
3. JuliaRegistrator bot will open a PR to the General Registry
4. The package name `Fatou` must not conflict with any existing registered package

Verify name availability at: https://github.com/JuliaRegistries/General

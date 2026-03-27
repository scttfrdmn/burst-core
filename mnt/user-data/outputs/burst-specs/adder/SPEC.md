# adder — Python Cloud Bursting Library

## Overview

`adder` is a Python package that provides cloud bursting to AWS for parallel workloads.
It implements the `concurrent.futures.Executor` interface as its primary API, making it
a drop-in replacement for `ProcessPoolExecutor`. It also provides a `joblib` backend,
enabling transparent cloud bursting for all scikit-learn parallel operations.

Read ARCHITECTURE.md before implementing. All session management, S3 key schema,
cost reporting format, error types, and worker lifecycle must conform to that spec.

---

## Repository Structure

```
adder/
├── adder/
│   ├── __init__.py              # Public API exports
│   ├── executor.py              # CloudExecutor (concurrent.futures.Executor)
│   ├── map.py                   # adder.map() convenience function
│   ├── pool.py                  # Pool class for reusable clusters
│   ├── session.py               # Session and DetachedSession
│   ├── joblib_backend.py        # joblib parallel backend
│   ├── env.py                   # Environment snapshot and Docker build
│   ├── worker.py                # Worker entrypoint (runs inside ECS container)
│   ├── serialize.py             # cloudpickle serialization helpers
│   ├── config.py                # Config file read/write (~/.burst/config.json)
│   ├── errors.py                # Exception hierarchy
│   ├── cost.py                  # Cost estimation and reporting
│   └── cli.py                   # `adder` CLI (setup, status, etc.)
├── tests/
│   ├── unit/
│   └── integration/             # Requires BURST_INTEGRATION_TEST=1
├── pyproject.toml
├── README.md
└── Dockerfile.worker            # Template Dockerfile for workers
```

---

## Language and Tooling

- Python 3.10+
- Package manager: `hatch` (pyproject.toml)
- AWS SDK: `boto3` >= 1.34
- Serialization: `cloudpickle` >= 3.0
- Progress display: `rich` (progress bar and cost display)
- CLI: `click` >= 8.0
- Joblib integration: `joblib` >= 1.3
- Type hints: full throughout, `mypy` clean

---

## Public API

### `adder.map()`

```python
def map(
    items: Iterable[T],
    fn: Callable[[T], U],
    *,
    workers: int = 10,
    cpu: int = 2,
    memory: str = "4GB",
    backend: Literal["fargate", "ec2"] = "fargate",
    spot: bool = False,
    max_cost: float | None = None,
    cost_alert: float | None = None,
    timeout: int | None = None,
    region: str | None = None,
) -> list[U]:
```

Synchronous. Blocks until all tasks complete. Returns results in original item order.

Raises:
- `BurstPartialError` if some tasks fail
- `BurstCostLimitError` if max_cost is hit
- `BurstQuotaError` if workers cannot be provisioned at requested count
- `BurstTimeoutError` if timeout is exceeded

### `CloudExecutor`

Implements `concurrent.futures.Executor` interface exactly.

```python
class CloudExecutor(concurrent.futures.Executor):
    def __init__(
        self,
        workers: int = 10,
        cpu: int = 2,
        memory: str = "4GB",
        backend: Literal["fargate", "ec2"] = "fargate",
        spot: bool = False,
        max_cost: float | None = None,
        region: str | None = None,
    ): ...

    def map(
        self,
        fn: Callable[[T], U],
        *iterables: Iterable[T],
        timeout: float | None = None,
        chunksize: int = 1,
    ) -> Iterator[U]: ...

    def submit(
        self,
        fn: Callable[..., T],
        /,
        *args: Any,
        **kwargs: Any,
    ) -> concurrent.futures.Future[T]: ...

    def shutdown(self, wait: bool = True, cancel_futures: bool = False) -> None: ...
```

Used as a context manager:
```python
with CloudExecutor(workers=50) as executor:
    results = list(executor.map(fn, items))
```

### `Pool`

Reusable cluster — provisions workers once, runs multiple map operations.

```python
class Pool:
    def __init__(
        self,
        workers: int = 10,
        cpu: int = 2,
        memory: str = "4GB",
        backend: Literal["fargate", "ec2"] = "fargate",
        spot: bool = False,
        region: str | None = None,
    ): ...

    def map(
        self,
        items: Iterable[T],
        fn: Callable[[T], U],
        **kwargs,
    ) -> list[U]: ...

    def shutdown(self) -> None: ...
```

### Detached Sessions

```python
# Start a detached session
session = adder.session(workers=50, detached=True)
session_id = session.submit(items, fn)
# Process exits — job continues in AWS

# Reattach
session = adder.attach(session_id)
status = session.status()   # Returns SessionStatus dataclass
results = session.collect() # Blocks until complete
session.cleanup()
```

`SessionStatus` is a dataclass matching the canonical schema in ARCHITECTURE.md.

---

## joblib Backend

Register as a named joblib backend so scikit-learn and any other joblib-using library
can burst transparently:

```python
# adder/__init__.py
from .joblib_backend import AdderBackend
import joblib
joblib.register_parallel_backend('adder', AdderBackend)
```

Usage:
```python
from joblib import parallel_backend
import adder  # import registers the backend

with parallel_backend('adder', workers=50, cpu=4):
    grid_search = GridSearchCV(model, param_grid, n_jobs=-1)
    grid_search.fit(X, y)
```

`AdderBackend` must implement the `joblib.parallel.ParallelBackendBase` interface.
The key method to implement is `effective_n_jobs()` and `apply_async()`.

This is the highest-leverage feature in the library. Implement it correctly.

---

## Serialization (`serialize.py`)

Use `cloudpickle` for all function and data serialization. This is non-negotiable —
standard `pickle` cannot serialize lambdas, closures, or interactively defined functions.

```python
import cloudpickle

def serialize_task(fn: Callable, items: list) -> bytes:
    """Serialize a function and its input data to bytes."""
    payload = {
        'fn': fn,
        'items': items,
        'python_version': sys.version_info[:3],
    }
    return cloudpickle.dumps(payload)

def deserialize_task(data: bytes) -> tuple[Callable, list]:
    """Deserialize a task payload."""
    payload = cloudpickle.loads(data)
    return payload['fn'], payload['items']

def serialize_result(result: Any) -> bytes:
    return cloudpickle.dumps(result)

def deserialize_result(data: bytes) -> Any:
    return cloudpickle.loads(data)
```

Task files (`.task`) contain the serialized `(fn, items_chunk)` tuple.
Result files (`.result`) contain the serialized result list for that chunk.

---

## Environment Snapshot (`env.py`)

### Capturing the environment

```python
def capture_environment() -> tuple[str, str]:
    """
    Returns (requirements_txt: str, env_hash: str).
    Captures currently installed packages using importlib.metadata.
    Excludes packages in EXCLUDE_PACKAGES list (pip, setuptools, wheel, etc.)
    Returns sorted 'package==version' lines joined by newline.
    env_hash is SHA256 of the requirements string.
    """
```

### Building the Docker image

Template `Dockerfile.worker`:
```dockerfile
FROM python:{python_version}-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY worker_entrypoint.py .
ENV PYTHONUNBUFFERED=1
CMD ["python", "worker_entrypoint.py"]
```

The `{python_version}` is the caller's exact Python version (e.g. `3.11.8` → `3.11`).

`env.py` must:
1. Call `capture_environment()` to get requirements and hash
2. Check ECR for existing image with that hash tag — skip build if present
3. If not present: render Dockerfile template, call `burst-core` to build and push
4. Return the ECR image URI

Shelling out to burst-core for Docker operations:
```python
import subprocess

result = subprocess.run(
    ['burst-core', 'image', 'build',
     '--lang', 'python',
     '--env-hash', env_hash,
     '--dockerfile', dockerfile_path],
    capture_output=True, text=True, check=True
)
ecr_uri = result.stdout.strip()
```

---

## Worker Entrypoint (`worker.py`)

This file runs inside the ECS container. It must be self-contained — no imports from
the rest of the adder package are allowed (the worker container might not have adder
installed, only the user's packages).

```python
#!/usr/bin/env python3
"""
adder worker entrypoint.
Reads environment variables, downloads task from S3, executes, uploads result.
"""
import os
import sys
import boto3
import cloudpickle

SESSION_ID = os.environ['BURST_SESSION_ID']
TASK_ID = os.environ['BURST_TASK_ID']
BUCKET = os.environ['BURST_S3_BUCKET']
REGION = os.environ['BURST_REGION']

def main():
    s3 = boto3.client('s3', region_name=REGION)
    keys = {
        'task': f'sessions/{SESSION_ID}/tasks/{TASK_ID}.task',
        'result': f'sessions/{SESSION_ID}/tasks/{TASK_ID}.result',
        'status': f'sessions/{SESSION_ID}/tasks/{TASK_ID}.status',
        'error': f'sessions/{SESSION_ID}/tasks/{TASK_ID}.error',
    }

    # Signal running
    s3.put_object(Bucket=BUCKET, Key=keys['status'], Body=b'running')

    try:
        # Download and deserialize task
        task_data = s3.get_object(Bucket=BUCKET, Key=keys['task'])['Body'].read()
        payload = cloudpickle.loads(task_data)
        fn = payload['fn']
        items = payload['items']

        # Execute
        results = [fn(item) for item in items]

        # Serialize and upload result
        result_data = cloudpickle.dumps(results)
        s3.put_object(Bucket=BUCKET, Key=keys['result'], Body=result_data)
        s3.put_object(Bucket=BUCKET, Key=keys['status'], Body=b'done')

    except Exception as e:
        import traceback
        error_msg = traceback.format_exc()
        s3.put_object(Bucket=BUCKET, Key=keys['error'], Body=error_msg.encode())
        s3.put_object(Bucket=BUCKET, Key=keys['status'], Body=b'failed')
        sys.exit(1)

if __name__ == '__main__':
    main()
```

The `worker_entrypoint.py` file is copied into the Docker image during build.

---

## Error Hierarchy (`errors.py`)

```python
class BurstError(Exception):
    """Base class for all adder errors."""

class BurstPartialError(BurstError):
    def __init__(self, results: list, errors: list):
        self.results = results      # list, None where task failed
        self.errors = errors        # list, None where task succeeded
        self.failed_count = sum(1 for e in errors if e is not None)
        self.success_count = sum(1 for r in results if r is not None)

class BurstQuotaError(BurstError):
    def __init__(self, requested: int, actual: int, quota_name: str, quota_value: float):
        self.requested_workers = requested
        self.actual_workers = actual
        self.quota_name = quota_name
        self.quota_value = quota_value

class BurstCostLimitError(BurstError):
    def __init__(self, limit: float, estimated: float, partial_results: list):
        self.limit = limit
        self.estimated_cost = estimated
        self.partial_results = partial_results

class BurstTimeoutError(BurstError):
    def __init__(self, session_id: str, timeout_seconds: int, status):
        self.session_id = session_id
        self.timeout_seconds = timeout_seconds
        self.status = status

class BurstSetupError(BurstError):
    def __init__(self, step: str, cause: str, remediation: str):
        self.step = step
        self.cause = cause
        self.remediation = remediation
```

---

## CLI (`cli.py`)

Entry point: `adder` command

```
adder setup              # delegates to burst-core setup
adder status             # delegates to burst-core status
adder session list       # delegates to burst-core session list
adder session status ID  # delegates to burst-core session status
adder session cleanup ID # delegates to burst-core session cleanup
adder config set KEY VAL # updates ~/.burst/config.json
adder config show        # prints current config
adder version            # prints adder version and burst-core version
```

All commands that delegate to `burst-core` must check for burst-core in PATH first
and print installation instructions if not found.

---

## `pyproject.toml`

```toml
[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[project]
name = "adder"
version = "0.1.0"
description = "Cloud bursting for Python — AWS parallel map"
readme = "README.md"
requires-python = ">=3.10"
license = {text = "Apache-2.0"}
dependencies = [
    "boto3>=1.34",
    "cloudpickle>=3.0",
    "rich>=13.0",
    "click>=8.0",
    "joblib>=1.3",
]

[project.optional-dependencies]
dev = ["pytest", "mypy", "ruff", "hatch"]

[project.scripts]
adder = "adder.cli:main"

[tool.hatch.build.targets.wheel]
packages = ["adder"]
```

---

## Testing

- Unit tests: mock all boto3 calls using `moto`
- Integration tests: require `BURST_INTEGRATION_TEST=1` and real AWS credentials
- Test the joblib backend with a real sklearn GridSearchCV (integration only)
- Test cloudpickle serialization of lambdas and closures (unit)
- Test correct result ordering with out-of-order S3 uploads (unit, using moto)
- 80%+ coverage on non-worker code

## Cost Display

Import and use the cost display format exactly as specified in ARCHITECTURE.md.
Use `rich.progress` for the progress bar during collection polling.
Use `rich.console` for all ✓, ⚠, 🚀, 💰 output.

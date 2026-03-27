# staRburst — Family Consistency Refactor

## Status: DEFERRED

**Do not implement this spec yet.**

staRburst is currently published on CRAN (v0.3.8) and is in active use.
This document captures the delta between the current implementation and full
burst family consistency. It is a planning document only.

Implement burst-core, adder, Fatou.jl, stet, and aji first. Once those are
stable, revisit this document and assess which changes are worth the CRAN
resubmission cycle.

---

## Changes Required for Full Consistency

### 1. Delegate setup to burst-core CLI

**Current**: `starburst_setup()` provisions AWS resources directly from R using paws.

**Target**: `starburst_setup()` becomes a thin wrapper that shells out to
`burst-core setup`, then reads `~/.burst/config.json`.

This ensures that R users who also use adder/Fatou.jl share the same AWS resources
and only need to run setup once regardless of which library they use first.

**Impact**: Medium. Requires verifying burst-core CLI is installed, printing
installation instructions if not. All AWS provisioning logic moves to burst-core.

### 2. Adopt canonical S3 key schema

**Current**: staRburst uses its own S3 key layout (document current layout here
when refactor begins).

**Target**: Adopt the canonical schema from ARCHITECTURE.md exactly.

**Impact**: Breaking change for existing sessions. Gate behind a new config option
or version bump. Old sessions remain accessible via legacy key reader.

### 3. Adopt canonical session ID format

**Current**: staRburst generates its own session IDs.

**Target**: `r_-{yyyymmdd}-{random-8hex}` per ARCHITECTURE.md.

**Impact**: Low for new sessions. Old session IDs remain accessible.

### 4. Structured error types

**Current**: staRburst uses `stop()` with string messages.

**Target**: S3 condition classes matching the burst family error hierarchy:

```r
# BurstPartialError
tryCatch(
  starburst_map(data, fn, workers = 100),
  starburst_partial_error = function(e) {
    cat("Failed:", e$failed_count, "tasks\n")
    # e$results — list, NULL where task failed
    # e$errors  — list, NULL where task succeeded
  },
  starburst_quota_error = function(e) {
    cat("Quota limited to", e$actual_workers, "workers\n")
  },
  starburst_cost_limit_error = function(e) {
    cat("Cost limit hit:", e$limit, "USD\n")
  }
)
```

**Impact**: Low. Additive change, does not break existing code that catches
generic errors.

### 5. Worker warmup / pre-compilation in container

**Current**: Workers load R packages on each cold start, taking 15–30 seconds
for heavy packages (tidyverse, etc.).

**Target**: Add a warmup layer to the generated Dockerfile that pre-loads
commonly used packages during image build:

```dockerfile
# After package installation:
RUN Rscript -e 'lapply(installed_packages, library, character.only=TRUE)'
```

Or more precisely, load only the packages in the renv.lock snapshot.

**Impact**: Low code change, meaningful UX improvement. Adds ~2 minutes to
first container build but reduces worker cold start from 15–30s to ~2s.

### 6. `SessionStatus` S3 class

**Current**: Session status is returned as a list.

**Target**: Return a proper S3 class with print method matching canonical
SessionStatus fields from ARCHITECTURE.md.

```r
status <- starburst_session_status(session_id)
print(status)
# <starburst_session_status>
#   session_id:      r_-20260315-a3f7b2c1
#   status:          running
#   tasks_complete:  234 / 1000
#   workers_active:  50
#   elapsed:         1m 23s
#   cost_estimate:   $0.14/hr
```

**Impact**: Low. Additive.

---

## Changes NOT Required

The following aspects of staRburst do NOT need to change:

- Package name (`starburst`) — keep as-is
- CRAN submission format and structure
- renv for environment snapshot — this is the correct R-native tool
- qs for serialization — correct choice for R
- `starburst_map()` / `starburst_cluster()` / `starburst_session()` API names
- EC2 + Fargate dual backend
- Cost display format — this is already the canonical format other libraries follow
- `starburst.ing` domain

---

## CRAN Considerations

Any breaking changes to S3 key schema or session ID format require:
- Increment to v0.4.0 (minor version for breaking change)
- NEWS.md entry documenting migration path
- Backward compatibility shim for reading old session IDs
- CRAN resubmission (0 errors, 0 warnings, 0 notes)

Non-breaking changes (error types, SessionStatus class, worker warmup):
- Increment to v0.3.9 or v0.3.10
- Standard CRAN resubmission

---

## Implementation Order (when this is eventually tackled)

1. Structured error types (lowest risk, highest value)
2. Worker warmup in Dockerfile
3. SessionStatus S3 class
4. Canonical session ID format (new sessions only, old sessions still readable)
5. burst-core setup delegation
6. Canonical S3 key schema (last, most disruptive)

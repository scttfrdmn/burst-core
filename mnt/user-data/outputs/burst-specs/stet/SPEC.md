# stet — TypeScript Cloud Bursting Library

## Overview

`stet` is a TypeScript/Node.js package that provides cloud bursting to AWS for parallel
async workloads. It uses esbuild to bundle user functions into self-contained JavaScript
files, eliminating the need for Docker-based environment capture in the common case.
The API is async-native throughout.

The name is a proofreader's mark: "let it stand." Submit your work and walk away.

Read ARCHITECTURE.md before implementing. All session management, S3 key schema,
cost reporting format, error types, and worker lifecycle must conform to that spec.

---

## Repository Structure

```
stet/
├── src/
│   ├── index.ts                 # Public API exports
│   ├── map.ts                   # map() convenience function
│   ├── pool.ts                  # Pool class
│   ├── session.ts               # Session and DetachedSession
│   ├── executor.ts              # Core ECS execution engine
│   ├── bundle.ts                # esbuild bundling logic
│   ├── serialize.ts             # Structured clone serialization
│   ├── env.ts                   # Environment detection (pure JS vs native modules)
│   ├── worker.ts                # Worker entrypoint (runs in ECS container)
│   ├── config.ts                # Config file (~/.burst/config.json)
│   ├── errors.ts                # Error class hierarchy
│   ├── cost.ts                  # Cost estimation and display
│   └── cli.ts                   # `stet` CLI
├── tests/
│   ├── unit/
│   └── integration/             # Requires BURST_INTEGRATION_TEST=1
├── package.json
├── tsconfig.json
├── README.md
└── Dockerfile.worker.native     # Fallback Dockerfile for native module deps
```

---

## Language and Tooling

- Node.js 20+ (LTS)
- TypeScript 5.4+
- AWS SDK: `@aws-sdk/client-ecs`, `@aws-sdk/client-s3`, `@aws-sdk/client-ecr`
- Bundler: `esbuild` >= 0.20
- CLI: built-in `parseArgs` from `node:util` (no external CLI framework)
- Testing: `vitest`
- Linting: `biome`

---

## The Bundle Strategy — Critical Design Decision

For **pure JavaScript/TypeScript** functions (no native `.node` addons):

1. Use `esbuild.build()` to bundle the user's function and all its imports
2. The result is a single `worker.bundle.js` file (~50–500KB typically)
3. Upload `worker.bundle.js` to S3 alongside task data
4. Worker container is a minimal Node.js image with no user packages installed
5. Worker downloads bundle and runs it with `node worker.bundle.js`

This means:
- **No Docker build for the common case** — worker container image is pre-built once
  and reused across all pure-JS stet runs
- **No npm install on workers** — the bundle has everything
- **Near-instant "environment sync"** — bundling takes milliseconds
- **Worker cold start is ~1–2 seconds** — just Node.js startup

For **native module dependencies** (packages with `.node` binary extensions):
- Detect by checking `node_modules` for any `.node` files in the dependency tree
- Fall back to Docker-based approach using `Dockerfile.worker.native`
- Print a warning explaining why the slower path is taken

### Bundle detection logic

```typescript
async function detectNativeModules(entryPoint: string): Promise<boolean> {
    // Run esbuild's dependency analysis
    // Check if any resolved module paths end in .node
    // Return true if native modules found
}
```

---

## Public API

All functions are async. Use TypeScript generics throughout.

### `map()`

```typescript
export async function map<T, U>(
    items: T[],
    fn: (item: T) => Promise<U> | U,
    options?: BurstOptions,
): Promise<U[]>
```

Options:
```typescript
interface BurstOptions {
    workers?: number          // default: 10
    cpu?: number              // vCPUs, default: 2
    memory?: string           // e.g. "4GB", default: "4GB"
    backend?: 'fargate' | 'ec2'  // default: 'fargate'
    spot?: boolean            // default: false
    maxCost?: number          // USD hard limit
    costAlert?: number        // USD warning threshold
    timeout?: number          // seconds
    region?: string           // overrides config
}
```

### `Pool`

```typescript
export class Pool {
    constructor(options?: BurstOptions)

    async map<T, U>(
        items: T[],
        fn: (item: T) => Promise<U> | U,
    ): Promise<U[]>

    async shutdown(): Promise<void>
}
```

### `session()` and `attach()`

```typescript
export function session(options?: BurstOptions & { detached?: boolean }): Session

export class Session {
    async submit<T, U>(
        items: T[],
        fn: (item: T) => Promise<U> | U,
    ): Promise<string>   // returns session_id

    async status(): Promise<SessionStatus>
    async collect<U>(): Promise<U[]>
    async cleanup(): Promise<void>
}

export async function attach(sessionId: string): Promise<Session>
```

### SessionStatus

```typescript
interface SessionStatus {
    sessionId: string
    language: 'ts'
    status: 'initializing' | 'running' | 'complete' | 'failed' | 'partial'
    tasksTotal: number
    tasksComplete: number
    tasksFailed: number
    workersActive: number
    elapsedSeconds: number
    costActual: number
    costEstimate: number
    createdAt: Date
    updatedAt: Date
}
```

---

## Serialization (`serialize.ts`)

TypeScript cannot serialize functions the way Python's cloudpickle can. Functions are
always bundled separately (via esbuild) from data. Task files contain only the data.

Use the structured clone algorithm for data serialization, which handles:
- Primitives (string, number, boolean, null, undefined)
- Objects and arrays (deeply nested)
- Date, Map, Set
- TypedArrays (Float32Array, Uint8Array, etc.) — important for numerical computing
- ArrayBuffer

Do NOT attempt to serialize: functions, class instances with methods, Promises, WeakMap.

```typescript
import { serialize, deserialize } from 'node:v8'

export function serializeData(data: unknown): Buffer {
    return serialize(data)
}

export function deserializeData(buffer: Buffer): unknown {
    return deserialize(buffer)
}
```

Task file format (`.task`):
```
[4 bytes: bundle length (big-endian uint32)]
[N bytes: worker.bundle.js content]
[4 bytes: items length (big-endian uint32)]
[M bytes: v8.serialize(items_chunk)]
```

Result file format (`.result`):
```
[v8.serialize(results_array)]
```

This layout allows the worker to read the bundle and data from a single file download.

---

## Bundle Generation (`bundle.ts`)

```typescript
import esbuild from 'esbuild'

export async function bundleFunction(
    fn: Function,
    options: BundleOptions,
): Promise<{ bundle: Buffer; hash: string; hasNativeModules: boolean }>

interface BundleOptions {
    platform: 'node'
    target: `node${string}`  // e.g. 'node20'
    format: 'cjs'
    minify: boolean           // default: false (easier debugging)
    sourcemap: boolean        // default: false for workers
}
```

Implementation approach:
1. Write the function to a temp `.ts` file using `fn.toString()`
2. Add a worker harness that calls the function and handles S3 I/O
3. Run esbuild on the temp file with `bundle: true`
4. Compute SHA256 of the bundle for caching
5. Clean up temp file

The worker harness template (injected at bundle time):
```typescript
// Injected by stet bundler
const __fn = {USER_FUNCTION};

// S3 read/execute/write is in the worker entrypoint
// __fn is called by the worker entrypoint
module.exports = { __fn };
```

---

## Worker Entrypoint (`worker.ts`)

This is compiled into the pre-built worker Docker image. It reads the task file,
extracts the bundle and data, executes the function, and uploads results.

```typescript
import { S3Client, GetObjectCommand, PutObjectCommand } from '@aws-sdk/client-s3'
import { serialize, deserialize } from 'node:v8'
import { writeFileSync, mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createRequire } from 'node:module'

const SESSION_ID = process.env.BURST_SESSION_ID!
const TASK_ID    = process.env.BURST_TASK_ID!
const BUCKET     = process.env.BURST_S3_BUCKET!
const REGION     = process.env.BURST_REGION!

const s3 = new S3Client({ region: REGION })

async function main() {
    const taskKey   = `sessions/${SESSION_ID}/tasks/${TASK_ID}.task`
    const resultKey = `sessions/${SESSION_ID}/tasks/${TASK_ID}.result`
    const statusKey = `sessions/${SESSION_ID}/tasks/${TASK_ID}.status`
    const errorKey  = `sessions/${SESSION_ID}/tasks/${TASK_ID}.error`

    await putS3(statusKey, 'running')

    try {
        // Download task file
        const taskBuf = await getS3(taskKey)

        // Parse task file format: [4b bundle len][bundle][4b data len][data]
        const bundleLen = taskBuf.readUInt32BE(0)
        const bundleSrc = taskBuf.slice(4, 4 + bundleLen)
        const dataLen   = taskBuf.readUInt32BE(4 + bundleLen)
        const itemsData = taskBuf.slice(4 + bundleLen + 4)
        const items     = deserialize(itemsData) as unknown[]

        // Write bundle to temp file and require it
        const tmpDir = mkdtempSync(join(tmpdir(), 'stet-'))
        const bundlePath = join(tmpDir, 'fn.cjs')
        writeFileSync(bundlePath, bundleSrc)
        const { __fn } = createRequire(import.meta.url)(bundlePath)

        // Execute function on each item
        const results = await Promise.all(items.map(item => __fn(item)))

        // Upload result
        await putS3(resultKey, serialize(results))
        await putS3(statusKey, 'done')

    } catch (err) {
        await putS3(errorKey, String(err))
        await putS3(statusKey, 'failed')
        process.exit(1)
    }
}

async function getS3(key: string): Promise<Buffer> { ... }
async function putS3(key: string, body: Buffer | string): Promise<void> { ... }

main()
```

The worker Docker image (`stet-worker`) is pre-built and pushed to ECR during
`burst-core setup`. It contains only Node.js and the AWS SDK — no user packages.
The user's bundle provides all application dependencies.

### Pre-built worker image

During `burst-core setup`, build and push a Node.js base worker image:

```dockerfile
FROM node:20-slim
WORKDIR /app
RUN npm install @aws-sdk/client-s3
COPY worker.js .
CMD ["node", "worker.js"]
```

Tag as `{ecr-base}/burst-workers-typescript:base`.

This image is reused for all pure-JS stet jobs. Only native-module jobs build a
custom image.

---

## Native Module Fallback

When native modules are detected:

```dockerfile
FROM node:20-slim
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --production
COPY worker.js .
CMD ["node", "worker.js"]
```

Environment hash is computed from `package-lock.json` SHA256.
Image is tagged with that hash and cached in ECR as usual.

Print warning:
```
⚠ Native modules detected in dependency tree. Using Docker-based environment sync.
  This requires a one-time image build (~2-3 minutes).
  Native modules: better-sqlite3, sharp, canvas
```

---

## Error Classes (`errors.ts`)

```typescript
export class BurstError extends Error {
    constructor(message: string) {
        super(message)
        this.name = 'BurstError'
    }
}

export class BurstPartialError extends BurstError {
    results: (unknown | null)[]
    errors: (Error | null)[]
    failedCount: number
    successCount: number
}

export class BurstQuotaError extends BurstError {
    requestedWorkers: number
    actualWorkers: number
    quotaName: string
    quotaValue: number
}

export class BurstCostLimitError extends BurstError {
    limit: number
    estimatedCost: number
    partialResults: unknown[]
}

export class BurstTimeoutError extends BurstError {
    sessionId: string
    timeoutSeconds: number
    status: SessionStatus
}

export class BurstSetupError extends BurstError {
    step: string
    cause: string
    remediation: string
}
```

---

## Cancellation

All async operations accept an optional `AbortSignal`:

```typescript
const controller = new AbortController()

// Cancel after 5 minutes
setTimeout(() => controller.abort(), 5 * 60 * 1000)

try {
    const results = await map(items, fn, {
        workers: 50,
        signal: controller.signal,
    })
} catch (err) {
    if (err.name === 'AbortError') {
        console.log('Job cancelled')
    }
}
```

When signal is aborted:
1. Stop polling for results
2. Call `burst-core session cleanup {session_id}` (best-effort)
3. Throw `AbortError`

---

## CLI (`cli.ts`)

Entry point: `stet` command (registered in package.json `bin`)

```
stet setup              # delegates to burst-core setup
stet status             # delegates to burst-core status
stet session list
stet session status <id>
stet session cleanup <id>
stet config set <key> <value>
stet config show
stet version
```

---

## `package.json`

```json
{
  "name": "stet",
  "version": "0.1.0",
  "description": "Cloud bursting for TypeScript — AWS parallel map",
  "type": "module",
  "main": "./dist/index.cjs",
  "module": "./dist/index.js",
  "types": "./dist/index.d.ts",
  "exports": {
    ".": {
      "import": "./dist/index.js",
      "require": "./dist/index.cjs",
      "types": "./dist/index.d.ts"
    }
  },
  "bin": {
    "stet": "./dist/cli.js"
  },
  "engines": {
    "node": ">=20"
  },
  "dependencies": {
    "@aws-sdk/client-ecs": "^3.0.0",
    "@aws-sdk/client-s3": "^3.0.0",
    "@aws-sdk/client-ecr": "^3.0.0",
    "esbuild": "^0.20.0"
  },
  "devDependencies": {
    "typescript": "^5.4.0",
    "vitest": "^1.0.0",
    "@biomejs/biome": "^1.0.0"
  }
}
```

Build outputs both ESM and CJS. Use `tsup` for the dual build.

---

## `tsconfig.json`

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "strict": true,
    "exactOptionalPropertyTypes": true,
    "noUncheckedIndexedAccess": true,
    "outDir": "./dist",
    "declaration": true,
    "declarationMap": true,
    "sourceMap": true
  },
  "include": ["src"],
  "exclude": ["node_modules", "dist", "tests"]
}
```

---

## Testing

- Unit tests with `vitest`
- Mock all AWS SDK calls with `vi.mock()`
- Test bundle generation with a simple function (unit)
- Test v8 serialization roundtrip for all supported types (unit)
- Test native module detection (unit, using fixture node_modules)
- Integration tests require `BURST_INTEGRATION_TEST=1` and real AWS credentials
- Test the pre-built worker image executes a bundled function correctly (integration)
- Test AbortController cancellation (integration)

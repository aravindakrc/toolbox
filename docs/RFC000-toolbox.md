# RFC000: Toolbox — an object-based developer toolbox

| | |
|---|---|
| **Status** | Accepted (v1 scope) — deferred items in §13 |
| **Created** | 2026-10-12 |

## 1. Summary

Toolbox is a general-purpose, distributable CLI (and MCP server) for automating repeated tasks a developer does.
It is designed to be **declarative** and **extensible**, with
a way to share your workflows with other developers.
It is built on one idea: **everything is an object**. A DOD, an AKS namespace, a GitHub repo, a local
repo clone, a Maven installation — each is an instance of a **Kind** with typed **fields**.

An object becomes *complete* when its fields are filled. Fields can be filled from several **sources**:
a command, another object's output, an env var, a value from the user, or a default. Objects can use
other objects to do their work. Some steps are manual (guided by a Markdown doc), others are automated.
Anyone in the org can add new Kinds, with no recompiling.

Nothing here is specific to microservices.

## 2. Goals / non-goals

**Goals**
- One small, stable engine; all domain knowledge lives in declarative Kind files.
- Every object can be inspected, completed and verified by a human or an agent.
- Manual and automated steps live side by side.
- Single binary, no runtime, no cluster-side install.

**Non-goals (v1)**
- Secrets handling (the field flag is reserved; implementation deferred).
- A UI or portal.
- Replacing tools like `kubectl`, `az`, `git`, `mise`; we call them.

## 3. Core concepts

| Concept | Meaning | Example |
|---|---|---|
| **Kind** | Schema + behaviour of a type of object | `AksNamespace` |
| **Object** | An instance of a Kind with field values | `AksNamespace/my-dod` |
| **Field** | A typed value on an object, with allowed sources | `cluster`, `name`, `path` |
| **Source** | Where a field value can come from | `flag`, `env`, `command`, `ref`, `prompt`, `default` |
| **Provider** | Code that knows how to `discover`, `apply`, `verify` an object | a shell script |
| **Task** | A named workflow made of steps over objects (manual or automated) | `local-setup` |

### 3.1 A Kind

Kinds are plain YAML files (block style only). Field types use JSON Schema type names (`string`,
`integer`, `boolean`, `enum`), but there is no JSON in the file.

```yaml
# kinds/local-repo.yaml
kind: LocalRepo
uses:
  repo: GithubRepo                    # a named slot that needs a GithubRepo object

fields:
  url:
    type: string
    required: true
    sources:
      - ref: repo.cloneUrl            # field of whichever object is bound to the `repo` slot
      - prompt: Repo clone URL        # ask the user if still missing

  path:
    type: string
    required: true
    sources:
      - flag: path
      - env: TBX_WORKSPACE
      - default: ${home}/code/${name}

  branch:
    type: string
    sources:
      - flag: branch
      - default: main

provider:
  discover: |                # exit 0 = already there
    test -d "$TBX_PATH/.git"
  apply: |                   # make it so
    git clone --branch "$TBX_BRANCH" "$TBX_URL" "$TBX_PATH"
  verify: |                  # exit 0 = healthy
    git -C "$TBX_PATH" rev-parse --is-inside-work-tree
```

**Providers** are the code behind an object. Each operation (`discover`, `apply`, `verify`) is a script,
written inline as a multiline string (above), or in a separate file with `file: apply.sh` when it gets long.

**Contract** (deliberately tiny):
- Field values are passed as environment variables: field `path` → `$TBX_PATH`.
- Exit code `0` means success / exists / healthy; non-zero means no / failed. Stderr is shown to the user.
- A script can publish output fields (for other objects to `ref`) by writing `key=value` lines to the file
  named by `$TBX_OUT`, e.g. `echo "cloneDir=$TBX_PATH" >> "$TBX_OUT"`.

Each source is one entry with one key (`flag`, `env`, `command`, `ref`, `prompt`, `default`). Built-in
variables available in values: `${name}`, `${home}`, `${os}`, `${arch}`, plus any other field.

### 3.2 Objects and field sources

Each field lists its sources **in order of precedence**. The first source that produces a value wins, and the
engine records *where* each value came from.

```yaml
# kinds/aks-namespace.yaml
kind: AksNamespace
uses:
  dod: Dod
fields:
  cluster:
    type: string
    required: true
    sources:
      - flag: cluster
      - env: AKS_CLUSTER
      - default: dev-aks
  name:
    type: string
    required: true
    sources:
      - flag: name
      - ref: dod.namespace
      - prompt: Namespace name
  context:
    type: string
    sources:
      - command: kubectl config current-context
```

```
$ tbx show AksNamespace/my-dod
FIELD     VALUE        FROM
cluster   dev-aks      default
name      my-dod       ref: dod → Dod/my-dod .namespace
context   (missing)    command failed: kubectl not logged in      ← incomplete
```

Source types:

| Source | Resolves from | Notes |
|---|---|---|
| `flag` | CLI flag / MCP argument | Highest precedence |
| `env` | Environment variable | `env: AKS_CLUSTER` |
| `command` | stdout of a command | `command: "git rev-parse --show-toplevel"` |
| `ref` | Field of the object bound to a `uses` slot | `ref: dod.namespace` builds the dependency graph |
| `prompt` | Asked of the user (CLI prompt, or MCP elicitation for agents) | Used last, only if still missing |
| `default` | Static value | Lowest precedence |

### 3.3 Objects using other objects

`uses` declares **named slots**, each requiring an object of a given Kind. A `ref` always goes through a
slot name (`ref: repo.cloneUrl`), never a Kind name, so it is unambiguous even with many objects of the same
Kind. The engine builds a graph and completes dependencies first.

```
AksNamespace ──► Dod ──► ServiceEnv ──► LocalRepo ──► MavenInstall
   (dod.namespace)   (repo.cloneUrl)   (java.version)
```

**Binding a slot to an object** (which `GithubRepo`?). The engine **never auto-binds**, even when only one
candidate exists. A slot is bound only by an explicit choice:

1. Declared on the object, passed on the CLI (`--use`), or set on a task step; or
2. Chosen by the user at a prompt. The engine lists the existing objects of that Kind (plus "create new")
   and asks. For an agent this is a `needs_input` result with the candidates, and the agent must ask the
   user. Once chosen, the binding is saved on the object, so the question is asked once.

```yaml
# objects/market-data-service.yaml  (instance of LocalRepo)
object: LocalRepo/market-data-service
uses:
  repo: GithubRepo/market-data-service      # explicit binding
set:
  branch: develop                           # fixed field value
```

```
$ tbx apply LocalRepo/pricing --use repo=GithubRepo/pricing-service
```

Two repos of the same Kind simply have different bindings:

```yaml
object: LocalRepo/pricing
uses: { repo: GithubRepo/pricing-service }
---
object: LocalRepo/market-data-service
uses: { repo: GithubRepo/market-data-service }
```

If a Kind needs several of the same Kind, it declares several slots (`primary: GithubRepo`,
`shared: GithubRepo`) or a list slot (`repos: [GithubRepo]`, read as `ref: repos[*].name`).

### 3.4 Manual steps

A provider operation (or a task step) can be `manual`: the engine shows a Markdown doc, waits for the
user to confirm, then runs `verify`.

```yaml
# in kind: Dod
provider:
  apply:
    manual: docs/create-dod.md      # screenshots live next to it
  verify: |
    kubectl get ns "$TBX_NAMESPACE"
```

```
$ tbx apply Dod/my-dod
Manual step: create the DOD in Jenkins. See docs/create-dod.md
Type the namespace when the pipeline finishes: █        ← fills field `namespace` (prompt)
Verifying... ✔ namespace "my-dod" exists
```

The doc is shown with `${field}` placeholders filled in from the object. A kind with no `discover` step is
treated as present once it has been applied, so a manual step is not repeated.

For agents the same step returns a structured "needs human" result containing the doc path and the
missing field, so the agent can ask the user rather than guess.

### 3.5 Recipes

A recipe is a named setup made of several objects, so that a whole task is one request instead of a long
prompt. It lists the inputs a user supplies and the objects to create, with the fields and slot bindings it
proposes.

```yaml
# recipes/local-service.yaml
recipe: local-service
inputs:
  - name: service
    description: Deployment name of the service in the DOD
  - name: repo
    default: ${service}
steps:
  - object: GithubRepo/${repo}
    set: { org: ciValue, name: ${repo} }
  - object: LocalRepo/${repo}
    use: { repo: GithubRepo/${repo} }
  - object: MirrordRun/${service}
    set: { service: ${service} }
    use: { repo: LocalRepo/${repo} }
```

Running a recipe is two steps, and slots are still never bound without the user:

1. **Plan.** `tbx recipe run NAME --input k=v` (or the `run_recipe` MCP tool) fills the inputs, marks each step
   `create` or `reuse` (an existing object is reused as it is) and prints the proposed fields and bindings.
   Nothing is created. Missing required inputs are reported like any other needed input.
2. **Approve.** Only after the user approves the wiring (`--yes`, the terminal prompt, or `approved` in the
   tool, which an agent may only set after the user said so) are the new objects created and then applied in
   order. A different binding is chosen with `--use Kind/name.slot=Kind/name`.

A plan that only reuses objects needs no approval, so a run that stopped on a manual step (such as `az login`)
is repeated as is. Recipes load from `./recipes`, `~/.toolbox/recipes`, `--recipes` and the directory next to
each `--kinds` directory, so an organisation distributes them next to its kinds.
### 3.6 Different OSes

OS differences are handled with one mechanism: a **`when`** condition on a source or a provider entry. The
first matching entry wins. Conditions use `os` (`windows|darwin|linux`) and optionally `arch`.

**Different field values per OS** — add `when` to a source:

```yaml
  path:
    type: string
    sources:
      - flag: path
      - default: C:\code\${name}
        when: { os: windows }
      - default: ${home}/code/${name}     # no `when` = applies everywhere else
```

**Different providers per OS** — scripts run in a **portable shell built into the `tbx` binary**
(a POSIX-style shell interpreter embedded in Go), so the same script runs on Windows, macOS and Linux
with no bash, WSL or Git-Bash installed. External programs it calls (`git`, `kubectl`, `az`, `mise`) run
natively. The `LocalRepo` scripts in §3.1 therefore work unchanged everywhere.

When a script genuinely needs OS-specific code, an operation can be a list with conditions and an explicit
`shell` (`sh` = the portable one, the default; `powershell`; `bash`; `cmd`):

```yaml
provider:
  apply:
    - when: { os: windows }
      shell: powershell
      run: |
        New-Item -ItemType Junction -Path $env:TBX_LINK -Target $env:TBX_TARGET
    - run: |                           # default: portable sh
        ln -s "$TBX_TARGET" "$TBX_LINK"
```

**Entire Kinds per OS** — when the difference is large, define separate Kinds and let a field choose, for
example `MavenInstall` using `mise` everywhere but a Windows-specific provider.

Guidelines:
- Prefer calling cross-platform tools directly from the provider, so most Kinds need no OS branches at all.
- Portable scripts may rely only on the shell language itself (variables, `test`, `if`, pipes, redirects,
  heredocs, `cd`, `echo`, `exit`, `set -e`, `command -v`) plus the built-in utilities below. Anything else
  (`sed`, `awk`, `grep`, ...) is not guaranteed on Windows: call a real cross-platform tool (`git`,
  `kubectl`, `az`, ...) or add a `when` branch.

**Built-in utilities** (shipped inside `tbx`, so identical on every OS; verified in prototype 1 on Windows):

| Built-in | Supported |
|---|---|
| `mkdir` | `-p` |
| `rm` | `-r`, `-f` (always recursive and forced in the prototype) |
| `cp` | single file to file |
| `cat` | files, or stdin |
| `ls` | names only, one per line |

`sed` and `grep` are candidates for a later addition via a Go coreutils library (not verified). The list is
intentionally short; it is documented, versioned with `tbx`, and grows only when a Kind needs it.
- `tbx doctor` reports which OS entries were selected, and `tbx show` shows the resolved value and which
  `when` matched.

## 4. Lifecycle

Every object goes through the same loop, whoever drives it:

```
 resolve fields ──► discover ──► apply (if needed) ──► verify
      │                │              │                  │
 sources in order   already there?   run provider     is it complete
 + dependencies     (idempotent)     or manual step   and healthy?
```

## 4.1 Where object data lives

Yes, plain YAML files, like Kubernetes manifests. Each object has two parts, split like k8s `spec` / `status`:

| Part | Holds | Written by | Edit by hand? | Commit to git? |
|---|---|---|---|---|
| **Declared** (`spec`) | `set` values, `uses` bindings, answers the user gave at prompts | user / engine (after a prompt) | Yes | No |
| **Observed** (`status`) | Resolved values + provenance, output fields, last `discover`/`verify` result and time | engine only | No | No |

```yaml
# ~/.toolbox/objects/LocalRepo/market-data-service/object.yaml   (declared)
object: LocalRepo/market-data-service
uses:
  repo: GithubRepo/market-data-service
set:
  branch: develop
```

```yaml
# ~/.toolbox/objects/LocalRepo/market-data-service/state.yaml    (observed, generated)
resolved:
  path:   { value: /home/me/code/market-data-service, from: "default" }
  url:    { value: git@github.com:org/market-data-service.git, from: "ref repo.cloneUrl" }
outputs:
  cloneDir: /home/me/code/market-data-service
lastVerify: { ok: true, at: 2026-10-12T18:10:00+05:30 }
```

**Location:** all object data is per user, under `~/.toolbox/objects/<Kind>/<name>/`: `object.yaml` (declared),
`state.yaml` (observed) and `data/` (files the object produces, exposed to scripts as `$TBX_DATA_DIR` and in
defaults as `${data}`). It is local data and is never committed. Sharing object manifests between users is deferred to a
later RFC.

**Rules**
- Observed state is a cache. It is always rebuildable by `discover`, so deleting `state.yaml` loses nothing.
- The declared file is the only source of truth for choices (bindings, answers); this is why a prompt is asked once.
- Secrets are never written to either file; the reserved `secret` flag will later point to a keychain or vault.
- Plain files keep it diffable and editable by agents.

## 5. CLI

```
tbx kinds                         # list available kinds
tbx new   <Kind>/<name> [--set k=v]   # create an object
tbx show  <Kind>/<name> [-o json]     # fields, values, provenance, what's missing
tbx apply <Kind>/<name> [--dry-run]   # complete dependencies, then apply
tbx verify <Kind>/<name>
tbx run   <task>                  # run a task
tbx doctor                        # check required external tools are installed
```

Rules so humans and agents both can use it:
- `-o json` everywhere, with stable exit codes.
- `--dry-run` on anything that changes things.
- Idempotent: running `apply` twice is safe.
- Non-interactive mode (`--no-prompt`) returns "missing fields" instead of blocking.

## 6. Agent / MCP

`tbx mcp` starts an MCP server generated from the same Kinds and Tasks. No separate logic.

| MCP tool | Does |
|---|---|
| `list_kinds` / `list_objects` | Discovery |
| `show_object` | Fields, provenance, missing fields |
| `verify_object` | Health check |
| `create_object` | Stores an object with field values and slot bindings |
| `apply_object` | Runs the lifecycle; returns `needs_input` or `needs_human` when stuck |
| `delete_object` | Runs `destroy`, removes the object; needs `confirm` after the user agrees |
| `list_recipes` | Lists the recipes and their inputs |
| `run_recipe` | Returns the plan (`needs_input`) until the user approves its wiring, then creates and applies the objects |

- Missing field or slot ? `needs_input` with the options. The agent asks the user, then calls again with
  `answers` (field values) or `use` (slot bindings). Progress so far is kept, so nothing is repeated.
- With `--elicit`, the client collects the answer from the user directly instead of the agent.
- Manual step ? `needs_human` with the instructions for the agent to present.
- `--read-only` hides `create_object`, `apply_object`, `delete_object` and `run_recipe`.
**How elicitation works** (verified with the official Go SDK v1.8.0, prototype 2):
- Tool input/output schemas are generated from Go structs, and output is returned as structured content.
- On the newest protocol (2026-07-28) a server may not send `elicitation/create` mid-call. The tool instead
  returns an `input_required` result carrying `InputRequests`; the client asks the user and retries the same
  call with `InputResponses`. The SDK client does this automatically.
- If the client does not support elicitation (or the user declines), the tool returns `status: needs_input`
  with `missing: [...]`, and the agent asks the user in chat.

## 7. Extending: adding a Kind

1. Add `kinds/<name>.yaml` (fields + sources).
2. Add the provider scripts inline under `provider:` (or in files next to the Kind if long).
3. Optionally add a Markdown doc for manual steps.
4. Open a PR; CI validates the schema.

Kinds can also be loaded from `~/.toolbox/kinds/` or an org git repo for private additions.

```yaml
# kinds/maven-install.yaml
kind: MavenInstall
fields:
  version:
    type: string
    sources:
      - flag: version
      - default: "3.9.6"
provider:
  discover: |
    mvn -v | grep -q "Apache Maven $TBX_VERSION"
  apply: |
    mise use --global maven@"$TBX_VERSION"
  verify: |
    mvn -v
```

## 8. Initial kinds

| Kind | Key fields | Provider |
|---|---|---|
| `Dod` | `services` (namespace and context from `AksNamespace`) | verified by deployments in the namespace; manual Jenkins step if missing |
| `AksNamespace` | `cluster`, `name`, `context` | `az aks get-credentials`; `az login` is manual |
| `ServiceEnv` | `service`, `file` | copies a deployment's env, `envFrom` and secret refs into a local `.env` |
| `GithubRepo` | `org`, `name`, `cloneUrl` | `gh`/git lookup |
| `LocalRepo` | `url`, `path`, `branch` | `git clone` |
| `MavenInstall` | `version`, `home` | `mise use maven@…` |
| `RunConfig` | `repo`, `ide`, `env`, `mirrord` | writes IntelliJ / VS Code config |
| `ConfigMapCopy` / `EnvSet` | `namespace`, `source`, `target` | `kubectl` copy / pull envs |

## 9. Implementation

**Go**, single static binary.
- Cross-compiles to Windows/macOS/Linux from one CI job.
- No runtime on the laptop; fast startup (good for MCP stdio servers).
- Strong at shelling out to `kubectl`, `az`, `git`, `mirrord`.
- Distributed via an Artifactory generic repo or GitHub Releases with checksums.

Layout:

```
toolbox/
├── docs/          RFCs, task docs, images
├── kinds/         Kind definitions (YAML)
├── kinds/         Kind definitions (YAML, scripts inline)├── tasks/         task definitions
├── cmd/tbx/       CLI entrypoint
└── internal/      resolver, graph, lifecycle, mcp
```

## 10. Phases

| Phase | Deliverable |
|---|---|
| 1 | Engine: Kind loader, field resolver (all sources), `show` / `apply` / `verify`, dependency graph |
| 2 | Kinds for tasks 1–4: `Dod` (manual), `AksNamespace`, `GithubRepo`, `LocalRepo`, `MavenInstall` |
| 3 | `RunConfig` (IntelliJ/VS Code), env pull, configmap copy, mirrord variant |
| 4 | `tbx mcp`, agent flow, read-only mode |
| 5 | Secrets support, org-repo kind loading, Jenkins trigger for `Dod` |

## 11. Risks

| Risk | Mitigation |
|---|---|
| Engine grows into a workflow platform | Keep the engine tiny; logic lives in Kinds and providers |
| Source precedence confuses users | `tbx show` always prints provenance |
| Provider scripts differ across OSes | Prefer calling tools directly; allow per-OS provider entries |
| Go MCP SDK maturity | Check before Phase 4; MCP layer is thin and replaceable |

## 12. Decisions from review

1. **Provider contract:** env vars + exit codes + `$TBX_OUT` is enough for v1 (§3.1).
2. **Storage:** plain YAML, declared/observed split, per user, never committed (§4.1). Sharing deferred.
3. **Source syntax and slot binding:** accepted as written (§3.2, §3.3).
4. **Where Kinds live:** in this repo (`kinds/`). Private/local Kinds from `~/.toolbox/kinds/` remain possible.
5. **OS conditions:** `when: { os, arch }` is enough for now (§3.6).
6. **Name:** `tbx`.

## 13. Open items (deferred)

- Slot fields exposed to scripts as `TBX_<SLOT>_<FIELD>`.
- Named one-shot `operations:` on a Kind (for actions such as copying data between DODs).
- A `dangerous: true` flag with forced confirmation, and a real `--dry-run`.
- Secrets handling (credentials for databases etc.).
- Sharing object manifests between users.
- Embedded-shell prototype: done (passes on Windows; built-in list in §3.6). Go MCP SDK prototype: done (see §6).
- Verify elicitation end-to-end in real clients (Copilot CLI, VS Code); only a test client was used so far.

# Using tbx in this repo

`tbx` (build with `go build -o tbx.exe ./cmd/tbx`) sets up and removes developer
environment objects such as cloned repos. Run it from the repo root so `./kinds` is found.
Prefer `tbx` over ad-hoc `git clone` or file deletion for anything it models.

## Rules

- Always pass `--no-prompt -o json` when running `tbx`; you cannot answer its prompts.
- Run `tbx show Kind/name` and `tbx apply --dry-run` before changing anything.
- Exit code 2 means input is needed. Read the `needs` list, ask the user, then rerun
  with `--set field=value` or `--use slot=Kind/name`.
- Never choose a slot object for the user, even when only one exists.
- Exit code 3 means a manual step is required. Walk the user through it.
- `tbx delete` needs `--yes`. Only pass it after the user has confirmed the deletion.
- Do not edit files under `~/.toolbox` by hand.

## Recipes: use them first

For a whole setup task, such as running a service locally with mirrord, do not build the objects one by one.
Call `list_recipes`, then `run_recipe` (CLI: `tbx recipe run NAME --input k=v`):

1. Ask the user for the recipe inputs it needs; never guess them.
2. Call `run_recipe` without `approved`. It returns the plan: which objects are created or reused, and the slot
   bindings it proposes. Show that plan to the user as a markdown table with one row per step and the columns Object, Action (create or reuse), Values (every field in set) and Uses (every slot binding, slot to object).
3. Only when the user approves the wiring (or names different bindings), call it again with `approved` set.
   Never set it yourself, and never choose a binding for them.
4. If it stops on `needs_input` or `needs_human`, handle that as usual and call `run_recipe` again.

Do not start the application or mirrord, and do not commit anything.

## Cloning a repo

```
tbx new GithubRepo/<name> --set org=<org> --set name=<repo>
tbx new LocalRepo/<name> --set path=<absolute path>
tbx apply LocalRepo/<name> --use repo=GithubRepo/<name>
```

Values given to `apply --set` are not saved; use `new --set` for anything that must persist.

## Removing it

```
tbx delete LocalRepo/<name> --yes
tbx delete GithubRepo/<name> --yes
```

Delete the `LocalRepo` first. Deleting it refuses to remove a clone with uncommitted
or unpushed work.

## When the tbx MCP tools are available

Prefer them over the shell commands above. The same rules apply: when a result has status `needs_input`,
ask the user and never choose a slot object for them; call `delete_object` with `confirm` only after the
user agrees; present `needs_human` messages to the user.

## Cluster access and service env

`AzLogin` -> `AksNamespace` -> `Dod` -> `ServiceEnv`. Each uses the one before it.
- `AksNamespace`: kube context for a namespace. Name the object after the namespace itself (the namespace
  field defaults to the object name) and share it between services: run `tbx list` first and reuse an existing
  one instead of creating another. The kube context is per cluster; an existing one is reused, never
  duplicated. `resourceGroup` is optional and only used when credentials must be fetched. Verified when
  deployments can be listed in the namespace.
- `Dod`: services expected in the namespace. Verified when each is a deployment there; if not, the manual
  Jenkins step shows (`needs_human`: show it, re-run once the user confirms).
- `ServiceEnv`: copies a service's env (inline, `envFrom` and secret refs) into a local `.env` file.
  Secrets are written as plain text, so never commit the file.

```
tbx new AzLogin/default
tbx new AksNamespace/<namespace> --set cluster=<cluster> --use login=AzLogin/default
tbx new Dod/<name> --set services=<a,b> --use ns=AksNamespace/<name>
tbx new ServiceEnv/<name> --set service=<a> --set file=<path> --use dod=Dod/<name>
tbx apply ServiceEnv/<name>
```

## Local run configuration

`RunConfig` (uses a `LocalRepo` and a `ServiceEnv`) writes an IntelliJ Spring Boot run configuration into the
repo's `.idea\runConfigurations`, loading the env file from the `ServiceEnv`. Objects keep their files in
`~/.toolbox/objects/<Kind>/<name>/data/` (`$TBX_DATA_DIR`, `${data}`); do not put them elsewhere.

```
tbx new RunConfig/<name> --set mainClass=<class> --use repo=LocalRepo/<name> --use env=ServiceEnv/<name>
tbx apply RunConfig/<name>
```

## Running against the cluster (mirrord)

Cluster-internal URLs do not resolve locally, so use `MirrordRun`. It writes a `mirrord.json` (mirror mode,
pod env) and a launcher into its data dir and a Shell Script run configuration into the repo's `.idea`.
`MirrordInstall` and `MavenInstall` download pinned, checksum-verified tools into their data dirs.

```
tbx new MirrordInstall/default
tbx new MavenInstall/default
tbx new MirrordRun/<name> --set service=<deployment> --use repo=LocalRepo/<name> --use dod=Dod/<dod> --use mirrord=MirrordInstall/default --use maven=MavenInstall/default
tbx apply MirrordRun/<name>
```

It also writes a Remote JVM Debug config ("<name> (attach debugger)", port 5005): start the mirrord run, then
attach to debug.

Never start the service through mirrord on the user's behalf without asking: it connects to the shared cluster.

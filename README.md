# toolbox
A CLI which has tools to automate workflows in local system

## Install

Releases are built by GitHub Actions when a `v*` tag is pushed. Each zip holds `tbx`, `kinds/`, `recipes/` and
`docs/` for one platform, plus a `checksums.txt`. The repository is private, so you need the
[GitHub CLI](https://cli.github.com) signed in with access (`gh auth login`).

Windows (PowerShell):

```
gh api repos/aravindakrc/toolbox/contents/scripts/install.ps1 -H "Accept: application/vnd.github.raw" > install.ps1
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

macOS and Linux:

```
gh api repos/aravindakrc/toolbox/contents/scripts/install.sh -H "Accept: application/vnd.github.raw" | sh
```

The script checks the checksum, puts `tbx` in `~/.toolbox/bin`, the kinds and recipes in `~/.toolbox/kinds` and
`~/.toolbox/recipes` (where `tbx` loads them from), and prints the MCP configuration. Run it again to update; it
replaces those two folders, so keep your own kinds in another directory and pass `--kinds`. Pass a tag
(`-Version v0.1.0` or `install.sh v0.1.0`) to pin a version.

You can also download the zip from the releases page and unpack it by hand.

Recipes such as `local-service` also need `git`, `az`, `kubectl`, `kubelogin` and `java` on the PATH.

To cut a release: `git tag v0.1.0 && git push origin v0.1.0`.

## Quick start

```
go build -o tbx ./cmd/tbx
tbx kinds
tbx new GithubRepo/toolbox --set org=aravindakrc --set name=toolbox
tbx new LocalRepo/toolbox
tbx apply LocalRepo/toolbox --dry-run
tbx apply LocalRepo/toolbox
tbx delete LocalRepo/toolbox     # runs the kind's destroy step, then removes the object
```

Recipes bundle a whole setup. `tbx recipe run` shows the plan and the slot bindings it proposes, and creates
nothing until you approve:

```
tbx recipe list
tbx recipe run local-service --input service=market-data-service \
  --input cluster=activate-us-development-aks --input namespace=<dod namespace>
```

Design: [docs/RFC000-toolbox.md](docs/RFC000-toolbox.md).

## Agent use (MCP)

`tbx mcp` serves the same objects to Copilot and other MCP clients over stdio. The install script prints the
configuration; register the binary by absolute path, since the client's working directory varies:

```json
{
  "servers": {
    "tbx": { "command": "C:/Users/you/.toolbox/bin/tbx.exe", "args": ["mcp"] }
  }
}
```

When working from a checkout instead, add `"--kinds", "C:/path/to/toolbox/kinds"` to `args`. Restart the MCP
server and start a new chat after installing or updating.

Agents get the same recipes through `list_recipes` and `run_recipe`.

Flags: `--read-only` hides the tools that change things; `--elicit` lets the client ask the user for missing
input (only for clients that support MCP elicitation).

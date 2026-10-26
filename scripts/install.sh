#!/bin/sh
# Installs tbx and its kinds and recipes from the latest GitHub release.
# Needs the GitHub CLI (gh), signed in with access to the repository.
# Usage: install.sh [version]   (default: latest; set REPO to override the repository)
set -eu

REPO="${REPO:-aravindakrc/toolbox}"
VERSION="${1:-latest}"
HOME_DIR="${TBX_HOME:-$HOME/.toolbox}"

command -v gh >/dev/null || { echo "Install the GitHub CLI first (https://cli.github.com) and run: gh auth login" >&2; exit 1; }

case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) echo "unsupported OS" >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "unsupported CPU" >&2; exit 1 ;; esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [ "$VERSION" = latest ]; then
  gh release download --repo "$REPO" --dir "$tmp" --pattern "tbx_*_${os}_${arch}.zip" --pattern checksums.txt
else
  gh release download "$VERSION" --repo "$REPO" --dir "$tmp" --pattern "tbx_*_${os}_${arch}.zip" --pattern checksums.txt
fi

zip="$(basename "$(ls "$tmp"/*.zip)")"
(cd "$tmp" && grep " $zip\$" checksums.txt | { sha256sum -c - 2>/dev/null || shasum -a 256 -c -; })

unzip -q "$tmp/$zip" -d "$tmp"
src="$tmp/${zip%.zip}"
mkdir -p "$HOME_DIR/bin"
cp "$src/tbx" "$HOME_DIR/bin/tbx"
chmod +x "$HOME_DIR/bin/tbx"
for d in kinds recipes; do
  rm -rf "${HOME_DIR:?}/$d"
  cp -R "$src/$d" "$HOME_DIR/$d"
done

echo
echo "Installed to $HOME_DIR"
echo "Add it to your PATH:  export PATH=\"$HOME_DIR/bin:\$PATH\""
echo
echo "MCP client configuration:"
printf '{\n  "servers": {\n    "tbx": { "command": "%s/bin/tbx", "args": ["mcp"] }\n  }\n}\n' "$HOME_DIR"
echo
echo "Restart the MCP server and start a new chat, then use the /tbx-<recipe> commands."

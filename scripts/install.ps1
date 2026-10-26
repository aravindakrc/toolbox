# Installs tbx and its kinds and recipes from the latest GitHub release.
# Needs the GitHub CLI (gh) signed in with access to the repository, or pass -Zip with a release zip downloaded from the browser.
param(
    [string]$Repo = "aravindakrc/toolbox",
    [string]$Version = "latest",
    [string]$Zip = ""
)
$ErrorActionPreference = "Stop"

if (-not $Zip -and -not (Get-Command gh -ErrorAction SilentlyContinue)) { throw "Install the GitHub CLI and run: gh auth login, or download the zip from the releases page and pass it with -Zip" }

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$home_ = if ($env:TBX_HOME) { $env:TBX_HOME } else { Join-Path $HOME ".toolbox" }
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("tbx-install-" + [guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null

try {
    if ($Zip) {
        $pkg = Get-Item $Zip
    } else {
        $tag = if ($Version -eq "latest") { @() } else { @($Version) }
        gh release download @tag --repo $Repo --dir $tmp --pattern "tbx_*_windows_$arch.zip" --pattern checksums.txt
        if ($LASTEXITCODE) { throw "download failed" }

        $pkg = Get-ChildItem $tmp -Filter "*.zip" | Select-Object -First 1
        $want = (Select-String -Path (Join-Path $tmp "checksums.txt") -Pattern ([regex]::Escape($pkg.Name))).Line.Split(" ")[0]
        if ((Get-FileHash $pkg.FullName -Algorithm SHA256).Hash -ne $want.ToUpper()) { throw "checksum mismatch for $($pkg.Name)" }
    }
    Expand-Archive $pkg.FullName -DestinationPath $tmp
    $src = Join-Path $tmp $pkg.BaseName
    $bin = Join-Path $home_ "bin"
    New-Item -ItemType Directory -Force $bin | Out-Null
    Copy-Item (Join-Path $src "tbx.exe") (Join-Path $bin "tbx.exe") -Force
    foreach ($dir in "kinds", "recipes") {
        $dest = Join-Path $home_ $dir
        if (Test-Path $dest) { Remove-Item $dest -Recurse -Force }
        Copy-Item (Join-Path $src $dir) $dest -Recurse
    }
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($userPath -split ";") -notcontains $bin) {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$bin", "User")
    Write-Host "Added $bin to your PATH. Open a new terminal to use tbx."
}

$exe = (Join-Path $bin "tbx.exe").Replace("\", "/")
Write-Host "`nInstalled to $home_`n"
Write-Host "MCP client configuration (VS Code mcp.json, or the MCP settings of your IDE):`n"
@{ servers = @{ tbx = @{ command = $exe; args = @("mcp") } } } | ConvertTo-Json -Depth 4
Write-Host "`nRestart the MCP server and start a new chat, then use the /tbx-<recipe> commands."

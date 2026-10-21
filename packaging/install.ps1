# Overclaud one-line installer for Windows:
#   irm https://overclaud.dev/install.ps1 | iex
# Downloads the latest OverclaudSetup.exe from GitHub Releases, checks it against the
# release's SHA256SUMS, and runs it.
$ErrorActionPreference = 'Stop'
$base = 'https://github.com/psumo/overclaud-installer/releases/latest/download'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$tmp = Join-Path $env:TEMP ("overclaud-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $sums = (Invoke-WebRequest -UseBasicParsing "$base/SHA256SUMS").Content -split "`n"
    $line = $sums | Where-Object { $_ -match " overclaud-[0-9][^ ]*-windows-$arch\.exe$" } | Select-Object -First 1
    if (-not $line) { throw "No installer for windows/$arch in the latest release" }
    $want, $name = ($line.Trim() -split '\s+', 2)
    $exe = Join-Path $tmp 'OverclaudSetup.exe'
    Write-Host "Downloading $name"
    Invoke-WebRequest -UseBasicParsing "$base/$name" -OutFile $exe
    $got = (Get-FileHash -Algorithm SHA256 $exe).Hash.ToLower()
    if ($got -ne $want.ToLower()) { throw "Checksum mismatch for $name; refusing to run it" }
    & $exe install @args
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

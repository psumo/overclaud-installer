# Overclaud native installer (v1.4.0)

One Go program, `overclaud`, built for Windows, macOS and Linux from one codebase. It replaces
`npx overclaud` (which needed Node before it could install Node) and the git-based auto-sync.

## Commands

| Command | Does |
|---|---|
| (no args, double-click) | Interactive install: asks for the licence key, then `install` |
| `install [--key K]` | Prerequisites, licence, download, verify, deploy, doctor, schedule updates |
| `update [--quiet]` | Same pipeline when a newer release exists; rolls back if doctor fails |
| `doctor [--live] [--explain]` | Checks the install; `--live` runs a `claude -p` smoke test; `--explain` asks Claude to diagnose failures |
| `status` | Version, licence, last update, pending conflicts |
| `version` | Installer version |

## Pipeline

1. **Prerequisites.** Required: git, Node >= 18, Python >= 3.9, Claude Code. Optional: uv.
   Found on PATH or installed with the platform's package manager (winget, Homebrew,
   apt/dnf/pacman/zypper), with direct downloads as the fallback for Node. Claude Code uses
   Anthropic's official installer. Every install step re-checks the version afterwards.
2. **Licence.** Same API as the npm CLI (`POST /api/download` with key, deviceId, platform).
   Reuses `~/.overclaud/device.json` and `~/.claude/store/.license.json`, so existing
   customers keep their one-device binding.
3. **Download and verify.** Fetch the release tarball into `~/.overclaud/staging/<sha>/`.
   The release contains `sync/manifest.json` (every file with its SHA-256) and
   `sync/manifest.sig` (Ed25519 over the manifest). The installer embeds the public key and
   refuses a release whose signature or file hashes do not match.
4. **Deploy with a customer layer.** `~/.overclaud/state.json` records the hash of every
   product file it deployed. On update, per file:
   - unchanged by the customer (hash equals what we deployed) -> replace with the new version
   - changed by the customer -> keep theirs, write the new one beside it as `<file>.overclaud-new`
   - removed from the product -> delete only if unchanged
   - never part of the product (`.git`, `store/`, `projects/`, customer skills) -> never touched
   `settings.json` is merged three ways (old product, new product, customer) so new hook
   registrations land and customer edits survive. A full backup of `~/.claude` is taken first.
5. **Platform fixes.** Hooks call `python`; where only `python3` exists they are rewritten.
6. **Doctor.** Prerequisite versions, manifest files present and unmodified, settings valid,
   every hook command resolves, the safety hook really blocks a dangerous command, other hooks
   exit 0 on sample input. Optional live `claude -p` smoke test. On failure `--explain` gives
   the log to `claude -p` and prints its suggested fix (never applied automatically).
7. **Auto-update.** Daily job (cron / launchd / Task Scheduler) running `overclaud update --quiet`.
   Update = stage, verify, deploy, doctor; on doctor failure restore the backup.
8. **Optional extras.** The release's own `sync/setup-prerequisites.{sh,ps1} --auto` installs
   extras (Playwright, Semgrep, ...); results are reported, failures are not fatal.

## Packaging

| Platform | Artefact | Notes |
|---|---|---|
| Windows | `OverclaudSetup.exe` (the program itself) | Copies itself to `%LOCALAPPDATA%\Overclaud`, adds to user PATH |
| macOS | `Overclaud.pkg` (universal: arm64 + amd64) | Installs `/usr/local/bin/overclaud`, opens Terminal to finish |
| Linux | `.deb`, `.tar.gz`, plain binary | `overclaud install` after the package installs |
| Any | `install.sh` / `install.ps1` one-liners | Download the right binary from GitHub Releases, verify its checksum |

Code signing is not set up yet (decision 2026-10-21): Windows SmartScreen and macOS Gatekeeper
will warn. Signing slots into `scripts/package.sh` later without code changes.

Hosting: GitHub Releases on the public repo `psumo/overclaud-installer` (installer only; the
harness itself stays licence-gated behind overclaud.dev).

## Testing

Unit tests for manifest, deploy, settings merge, prerequisite parsing. Integration test runs a
full install against a local fake licence server in a clean container. GitHub Actions runs the
same on windows-latest, macos-latest and ubuntu-latest.

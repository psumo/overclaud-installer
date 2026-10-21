# Overclaud installer

Installs and updates [Overclaud](https://overclaud.dev), the self-improving harness for
Claude Code. It sets up what Overclaud needs (Git, Node.js, Python, Claude Code), checks your
licence, downloads the harness, verifies its signature, installs it into `~/.claude` without
overwriting your own files, checks that everything works, and keeps it updated.

This repository holds only the installer. The harness itself is licence-gated and downloaded
from overclaud.dev during install.

## Install

Download the file for your computer from the
[latest release](https://github.com/psumo/overclaud-installer/releases/latest):

| Computer | File |
|---|---|
| Windows | `OverclaudSetup.exe` |
| Mac (Apple silicon or Intel) | `Overclaud.pkg` |
| Ubuntu / Debian | `overclaud_<version>_amd64.deb` or `_arm64.deb` |
| Other Linux | `overclaud-<version>-linux-<arch>.tar.gz` |

Or in a terminal:

```sh
# macOS / Linux
curl -fsSL https://github.com/psumo/overclaud-installer/releases/latest/download/install.sh | sh
```

```powershell
# Windows
irm https://github.com/psumo/overclaud-installer/releases/latest/download/install.ps1 | iex
```

The installers are not code-signed yet, so Windows and macOS warn on first run (see the
release notes for the one click each needs).

## Commands

| Command | Does |
|---|---|
| `overclaud install` | Install, or reinstall over an existing setup |
| `overclaud update` | Install the newest release; rolls back if its checks fail |
| `overclaud doctor` | Check the install; `--live` also runs a real Claude session, `--explain` asks Claude to diagnose failures |
| `overclaud status` | Version, licence and any of your edits that have a newer product version waiting |

## How updates treat your files

- Files Overclaud never shipped (your skills, memories, `.git`, `store/`) are never touched.
- A product file you edited is kept; the new version is saved next to it as `<file>.overclaud-new`.
- `settings.json` is merged: new hooks arrive, your own settings stay.
- Every update is backed up first, verified against Overclaud's signing key, checked by
  `doctor` (including that the safety hook still blocks dangerous commands), and rolled back
  if a check fails. Older releases are refused.

## Development

```sh
go test ./...                                   # unit tests
go test -tags e2e ./test/e2e -v                 # full install/update/rollback on this OS
scripts/e2e-linux.sh /path/to/overclaud         # same, in a clean Ubuntu container, real payload
scripts/package.sh 1.4.0                        # build all artefacts into dist/
```

Releases are signed with `cmd/overclaud-release` on the maintainer's machine; see
[docs/PLAN.md](docs/PLAN.md).

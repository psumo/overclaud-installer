Pick the file for your computer:

| Computer | Download | Then |
|---|---|---|
| Windows | `OverclaudSetup.exe` | Double-click it and enter your licence key. |
| Mac | `Overclaud.pkg` | Open it; a Terminal window asks for your licence key. |
| Ubuntu / Debian | `overclaud_<version>_amd64.deb` (or `arm64`) | `sudo apt install ./overclaud_*.deb`, then `overclaud install` |
| Other Linux | `overclaud-<version>-linux-amd64.tar.gz` | Unpack, then `./overclaud install` |

Or one line in a terminal:

- macOS / Linux: `curl -fsSL https://github.com/psumo/overclaud-installer/releases/latest/download/install.sh | sh`
- Windows (PowerShell): `irm https://github.com/psumo/overclaud-installer/releases/latest/download/install.ps1 | iex`

These installers are not code-signed yet. Windows may show "Windows protected your PC": click
**More info**, then **Run anyway**. macOS may say it cannot verify the developer: open
**System Settings > Privacy & Security** and click **Open Anyway**. Every download of the
harness itself is verified against Overclaud's signing key before anything is installed.
Check any file against `SHA256SUMS`.

#!/usr/bin/env bash
# End-to-end test on a clean Ubuntu container: real prerequisite installs (apt, the official
# Claude Code installer), a fake licence server, signed test releases.
#
# Scenarios: fresh install (prerequisites incl. bun, MCP servers, plugins, key store,
# device check); doctor; customer edit kept across an update; downgrade refused; an
# update whose safety hook is broken fails its checks and is rolled back.
#
#   scripts/e2e-linux.sh /path/to/overclaud-repo [ubuntu:24.04]
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
repo="$1"; image="${2:-ubuntu:24.04}"
work="$(mktemp -d)"
cleanup() { [ -n "${api_pid:-}" ] && kill "$api_pid" 2>/dev/null; find "$work" -mindepth 1 -delete 2>/dev/null || true; }
trap cleanup EXIT
export PATH="$HOME/.local/go/bin:$PATH"

echo "== build"
export OVERCLAUD_SIGNING_KEY="$work/test.key"
( cd "$here" && go build -o "$work/bin/overclaud-release" ./cmd/overclaud-release && go build -o "$work/bin/fakeapi" ./test/fakeapi )
export PATH="$work/bin:$PATH"
pub="$(overclaud-release keygen)"
( cd "$here" && CGO_ENABLED=0 go build -ldflags "-X github.com/psumo/overclaud-installer/internal/app.ReleasePublicKey=$pub -X github.com/psumo/overclaud-installer/internal/app.Version=e2e" -o "$work/overclaud" ./cmd/overclaud )

echo "== releases"
"$here/scripts/make-test-release.sh" "$repo" "$work/r1.tgz" 9.0.1
"$here/scripts/make-test-release.sh" "$repo" "$work/r2.tgz" 9.0.2 touch-file
"$here/scripts/make-test-release.sh" "$repo" "$work/r3.tgz" 9.0.3 break-safety-hook
cp "$work/r1.tgz" "$work/served.tgz"

port=$((20000 + RANDOM % 20000))
fakeapi -addr "127.0.0.1:$port" -sha r1 -tarball "$work/served.tgz" >"$work/api.log" 2>&1 &
api_pid=$!
sleep 1

cat > "$work/inside.sh" <<'INSIDE'
set -euo pipefail
api="$1"
export OVERCLAUD_API="$api/api"
pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; exit 1; }
switch() { cp "/work/$1.tgz" /work/served.tgz; curl -s "$api/admin/sha?v=$1" >/dev/null; }

echo "--- fresh install"
# An unsafe entry the old install.sh wrote, which the installer must remove (with a backup).
printf '{"mcpServers": {"github": {"command": "npx", "args": ["--yes", "-p", "github-mcp-server", "github-mcp-server-mcp"]}, "mine": {"command": "keep-me"}}}\n' > ~/.mcp.json
/work/overclaud install --key GC-TEST-0000-0000 --no-extras </dev/null
grep -q '"python3 ' ~/.claude/settings.json && pass "hooks point at python3" || fail "hooks still use python"
[ -f ~/overclaud/FIRST-RUN.md ] && pass "product dir in place" || fail "no ~/overclaud"
[ "$(stat -c %a ~/.claude/store/.license.json)" = 600 ] && pass "licence file 0600" || fail "licence file perms"
command -v overclaud >/dev/null || export PATH="$HOME/.local/bin:$PATH"
overclaud doctor && pass "doctor after install" || fail "doctor after install"
crontab -l 2>/dev/null | grep -q overclaud-auto-update && pass "daily update scheduled" || echo "NOTE: no cron in this image (expected in minimal containers)"

echo "--- v1.4.1: tools, MCP servers, plugins"
export PATH="$HOME/.bun/bin:$PATH"
command -v bun >/dev/null && pass "bun installed ($(bun --version))" || fail "bun missing"
python3 - <<'PY' && pass "MCP servers registered for the user" || fail "MCP servers"
import json, os
s = json.load(open(os.path.expanduser("~/.claude.json")))["mcpServers"]
gh = s["github"]
assert gh.get("type") == "http" and "api.githubcopilot.com" in gh["url"], gh
assert "keys header github" in gh.get("headersHelper", ""), gh
assert not any(k.lower() in ("authorization",) for k in (gh.get("headers") or {})), "token stored in config"
assert "claude-flow@" in " ".join(s["claude-flow"].get("args", [])), s["claude-flow"]
assert "gitnexus" not in s
PY
grep -q github-mcp-server ~/.mcp.json && fail "unsafe github entry left in ~/.mcp.json" || pass "unsafe ~/.mcp.json entry removed"
grep -q keep-me ~/.mcp.json && pass "customer's own ~/.mcp.json entry kept" || fail "customer entry lost"
ls ~/.mcp.json.bak-* >/dev/null 2>&1 && pass "~/.mcp.json backed up" || fail "no ~/.mcp.json backup"
claude plugin list --json | grep -q '"claude-mem@thedotmack"' && pass "claude-mem installed" || fail "claude-mem missing"
python3 - <<'PY' && pass "claude-mem is the reviewed commit" || fail "claude-mem commit"
import json, os
d = json.load(open(os.path.expanduser("~/.claude/plugins/installed_plugins.json")))
rec = [r for r in d["plugins"]["claude-mem@thedotmack"] if r["scope"] == "user"][0]
want = [p["commit"] for p in json.load(open(os.path.expanduser("~/overclaud/sync/mcp-servers.json")))["plugins"] if p["id"] == "claude-mem@thedotmack"][0]
assert rec["gitCommitSha"] == want, (rec["gitCommitSha"], want)
PY

echo "--- v1.4.1: key store"
overclaud keys list
printf 'exa-test-key\n' | overclaud keys set exa
[ "$(cat ~/.overclaud/secrets-backend)" = file ] && pass "no Secret Service here, file store chosen" || fail "backend choice"
[ "$(stat -c %a ~/.overclaud/secrets)" = 700 ] && [ "$(stat -c %a ~/.overclaud/secrets/exa)" = 600 ] && pass "key store is private" || fail "key store perms"
grep -q exa-test-key ~/.claude/settings.json ~/.claude.json 2>/dev/null && fail "key leaked into settings" || pass "key not in any settings file"
CLAUDE_ENV_FILE=/tmp/envfile overclaud keys session-env
grep -q "export EXA_API_KEY='exa-test-key'" /tmp/envfile && pass "session hook exports the skill key" || fail "session-env"
out="$(overclaud with-keys exa -- sh -c 'echo "len=${#EXA_API_KEY}"')"
[ "$out" = "len=12" ] && pass "with-keys passes the key to one process" || fail "with-keys: $out"
grep -q '"overclaud keys session-env' ~/.claude/settings.json && pass "session key hook registered" || fail "no session key hook"

echo "--- v1.4.1: device check (not signed in to Claude here)"
overclaud doctor --device </dev/null | tee /tmp/device.txt
grep -q "Settings location" /tmp/device.txt && grep -q "not signed in" /tmp/device.txt && pass "device check runs and reports the missing sign-in" || fail "device check output"

echo "--- customer edit survives an update"
echo "my own note" >> ~/.claude/rules/common/common-testing.md
switch r2
overclaud update
grep -q "my own note" ~/.claude/rules/common/common-testing.md && pass "customer edit kept" || fail "customer edit lost"
[ -f ~/.claude/rules/common/common-testing.md.overclaud-new ] && pass "new version written beside it" || fail "no .overclaud-new"
grep -q "9.0.2" ~/.claude/rules/common/common-patterns.md && pass "untouched file updated" || fail "untouched file not updated"
overclaud status

echo "--- an older signed release is refused"
switch r1
if overclaud update; then fail "downgrade to 9.0.1 accepted"; fi
grep -q "9.0.2" ~/.claude/rules/common/common-patterns.md && pass "downgrade refused, 9.0.2 kept" || fail "downgrade changed files"

echo "--- broken update is rolled back"
before="$(sha256sum ~/.claude/hooks/scripts/pre-tool-use.py | cut -d' ' -f1)"
switch r3
if overclaud update; then fail "broken update was accepted"; fi
after="$(sha256sum ~/.claude/hooks/scripts/pre-tool-use.py | cut -d' ' -f1)"
[ "$before" = "$after" ] && pass "safety hook restored" || fail "rollback did not restore the safety hook"
grep -q "9.0.2" ~/.claude/rules/common/common-patterns.md && pass "previous release still in place" || fail "rollback incomplete"
overclaud doctor && pass "doctor after rollback" || fail "doctor after rollback"
echo "ALL E2E CHECKS PASSED"
INSIDE

echo "== container ($image)"
docker run --rm --network host -v "$work:/work" "$image" bash -c "
  set -e
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq >/dev/null && apt-get install -y -qq sudo ca-certificates >/dev/null
  useradd -m -s /bin/bash tester
  echo 'tester ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/tester
  chmod -R a+rwX /work
  su - tester -c 'bash /work/inside.sh http://127.0.0.1:$port'
"

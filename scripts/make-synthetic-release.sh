#!/usr/bin/env bash
# A small stand-in for the Overclaud payload (the real one is private), with the parts the
# installer cares about: settings.json hooks, a safety hook that blocks download-and-run,
# a friction hook, rules, docs and the sync/ scripts. Used by CI on all three platforms.
#
#   make-synthetic-release.sh <out-dir>
set -euo pipefail
out="$1"
mkdir -p "$out/.claude/hooks/scripts" "$out/.claude/rules/common" "$out/.claude/skills/hello" "$out/sync"
cat > "$out/.claude/settings.json" <<'EOF'
{
  "env": {"PYTHONUTF8": "1"},
  "effortLevel": "medium",
  "hooks": {
    "PreToolUse": [{"matcher": "Bash|Read|Write|Edit", "hooks": [{"type": "command", "command": "python \"$HOME/.claude/hooks/scripts/pre-tool-use.py\""}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "python \"$HOME/.claude/hooks/scripts/friction-sense.py\"", "timeout": 5}]}]
  }
}
EOF
cat > "$out/.claude/hooks/scripts/pre-tool-use.py" <<'EOF'
import json, re, sys
event = json.load(sys.stdin)
cmd = (event.get("tool_input") or {}).get("command", "")
if re.search(r"(curl|wget)[^|]*\|\s*(ba)?sh", cmd):
    print("BLOCKED: pipe-to-shell", file=sys.stderr)
    sys.exit(2)
EOF
cat > "$out/.claude/hooks/scripts/friction-sense.py" <<'EOF'
import sys
sys.stdin.read()
EOF
printf '# Testing\n\nWrite tests first.\n' > "$out/.claude/rules/common/common-testing.md"
printf '# Patterns\n\nKeep it simple.\n' > "$out/.claude/rules/common/common-patterns.md"
printf -- '---\nname: hello\ndescription: test skill\n---\nSay hello.\n' > "$out/.claude/skills/hello/SKILL.md"
printf '# First run\n\nRead me.\n' > "$out/FIRST-RUN.md"
printf '#!/usr/bin/env bash\necho "no extras in the synthetic release"\n' > "$out/sync/setup-prerequisites.sh"
printf 'Write-Host "no extras in the synthetic release"\n' > "$out/sync/setup-prerequisites.ps1"
chmod +x "$out/sync/setup-prerequisites.sh"

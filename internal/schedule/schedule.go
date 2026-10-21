// Package schedule registers the daily `overclaud update --quiet` job with the platform's
// scheduler (cron or a systemd user timer on Linux, launchd on macOS, Task Scheduler on
// Windows) and removes the old git-based auto-sync it replaces.
package schedule

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	cronMarker    = "# overclaud-auto-update"
	oldCronMarker = "# claude-config auto-sync"
	launchdLabel  = "dev.overclaud.update"
	oldLaunchd    = "com.claude.config-sync"
	winTask       = "Overclaud Update"
	oldWinTask    = "Claude Config Auto-Sync"
)

// Slot spreads customers over the night so update checks do not all arrive at once.
func Slot(deviceID string) (hour, minute int) {
	h := fnv.New32a()
	h.Write([]byte(deviceID))
	v := h.Sum32()
	return 2 + int(v%4), int((v / 4) % 60)
}

// CronLines returns the new crontab: old auto-sync and earlier overclaud lines removed,
// one fresh overclaud line added.
func CronLines(current, bin string, hour, minute int) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(current, "\n"), "\n") {
		if l == "" || strings.Contains(l, cronMarker) || strings.Contains(l, oldCronMarker) || strings.Contains(l, "sync/auto-sync.sh") {
			continue
		}
		keep = append(keep, l)
	}
	keep = append(keep, fmt.Sprintf("%d %d * * * %q update --quiet >/dev/null 2>&1 %s", minute, hour, bin, cronMarker))
	return strings.Join(keep, "\n") + "\n"
}

// LaunchdPlist is the macOS agent definition.
func LaunchdPlist(bin string, hour, minute int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>update</string><string>--quiet</string></array>
  <key>StartCalendarInterval</key>
  <dict><key>Hour</key><integer>%d</integer><key>Minute</key><integer>%d</integer></dict>
  <key>RunAtLoad</key><false/>
</dict>
</plist>
`, launchdLabel, xmlEscape(bin), hour, minute)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Install registers the daily job for bin and returns a one-line description.
func Install(bin, home, deviceID string) (string, error) {
	hour, minute := Slot(deviceID)
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("schtasks", "/Delete", "/TN", oldWinTask, "/F").Run()
		tr := fmt.Sprintf(`"%s" update --quiet`, bin)
		out, err := exec.Command("schtasks", "/Create", "/TN", winTask, "/TR", tr, "/SC", "DAILY",
			"/ST", fmt.Sprintf("%02d:%02d", hour, minute), "/F").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("Task Scheduler: %s", strings.TrimSpace(string(out)))
		}
		return fmt.Sprintf("Task Scheduler task %q, daily at %02d:%02d", winTask, hour, minute), nil
	case "darwin":
		agents := filepath.Join(home, "Library", "LaunchAgents")
		uid := fmt.Sprint(os.Getuid())
		oldPlist := filepath.Join(agents, oldLaunchd+".plist")
		if _, err := os.Stat(oldPlist); err == nil {
			_ = exec.Command("launchctl", "bootout", "gui/"+uid, oldPlist).Run()
			_ = os.Remove(oldPlist)
		}
		plist := filepath.Join(agents, launchdLabel+".plist")
		if err := os.MkdirAll(agents, 0o755); err != nil {
			return "", err
		}
		_ = exec.Command("launchctl", "bootout", "gui/"+uid, plist).Run()
		if err := os.WriteFile(plist, []byte(LaunchdPlist(bin, hour, minute)), 0o644); err != nil {
			return "", err
		}
		if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, plist).CombinedOutput(); err != nil {
			return "", fmt.Errorf("launchd: %s", strings.TrimSpace(string(out)))
		}
		return fmt.Sprintf("launchd agent %s, daily at %02d:%02d", launchdLabel, hour, minute), nil
	default:
		if _, err := exec.LookPath("crontab"); err == nil {
			cur, _ := exec.Command("crontab", "-l").Output()
			c := exec.Command("crontab", "-")
			c.Stdin = strings.NewReader(CronLines(string(cur), bin, hour, minute))
			if out, err := c.CombinedOutput(); err != nil {
				return "", fmt.Errorf("crontab: %s", strings.TrimSpace(string(out)))
			}
			return fmt.Sprintf("cron, daily at %02d:%02d", hour, minute), nil
		}
		return systemdTimer(bin, home, hour, minute)
	}
}

func systemdTimer(bin, home string, hour, minute int) (string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", errors.New("no cron or systemd found; run `overclaud update` yourself now and then")
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	svc := fmt.Sprintf("[Unit]\nDescription=Overclaud update\n\n[Service]\nType=oneshot\nExecStart=%q update --quiet\n", bin)
	tmr := fmt.Sprintf("[Unit]\nDescription=Daily Overclaud update\n\n[Timer]\nOnCalendar=*-*-* %02d:%02d:00\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n", hour, minute)
	if err := os.WriteFile(filepath.Join(dir, "overclaud-update.service"), []byte(svc), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "overclaud-update.timer"), []byte(tmr), 0o644); err != nil {
		return "", err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", "overclaud-update.timer").CombinedOutput(); err != nil {
		return "", fmt.Errorf("systemd: %s", strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("systemd user timer, daily at %02d:%02d", hour, minute), nil
}

// render.go — pure template rendering for the generated unit / plist / env files.
// These functions take a fully-resolved Config and return file contents as strings.
// They perform NO I/O, so they are exhaustively unit-tested (including a guarantee that
// no machine-specific path leaks into the output).
package fleet

import (
	"path/filepath"
	"strings"
	"text/template"
)

// renderData is the flattened view passed to the templates.
type renderData struct {
	User          string
	UserDirective bool // emit `User=` (system scope only)
	Home          string
	Binary        string
	EnvFile       string
	Timeout       int
	BootSec       int
	ActiveSec     int
	Interval      int
	WantedBy      string
	Label         string
	LogDir        string
	PATH          string
}

func (c *Config) data() renderData {
	wantedBy := "default.target"
	if c.Scope == ScopeSystem {
		wantedBy = "multi-user.target"
	}
	return renderData{
		User:          c.User,
		UserDirective: c.Scope == ScopeSystem,
		Home:          c.Home,
		Binary:        c.Binary,
		EnvFile:       c.EnvFile,
		Timeout:       c.Timeout,
		BootSec:       c.BootSec,
		ActiveSec:     c.ActiveSec,
		Interval:      c.Interval,
		WantedBy:      wantedBy,
		Label:         launchdLabel,
		LogDir:        filepath.Join(c.Home, ".agent-hub"),
		PATH:          c.PATH,
	}
}

func render(name, tmpl string, d renderData) (string, error) {
	t, err := template.New(name).Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

func renderSystemdService(c *Config) (string, error) {
	return render("service", systemdServiceTmpl, c.data())
}

func renderSystemdTimer(c *Config) (string, error) {
	return render("timer", systemdTimerTmpl, c.data())
}

func renderLaunchdPlist(c *Config) (string, error) {
	return render("plist", launchdPlistTmpl, c.data())
}

func renderEnvScaffold(c *Config) string {
	s, _ := render("env", envScaffoldTmpl, c.data())
	return s
}

const systemdServiceTmpl = `[Unit]
Description=agent-hub bridge fleet — boot-start + watchdog (agenthubctl bridge start --all)
Documentation=https://github.com/kishibashi3/agent-hub-control
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
{{- if .UserDirective}}
User={{.User}}
{{- end}}
WorkingDirectory={{.Home}}
# Secrets live in the EnvironmentFile (mode 0600), never in this unit. Leading "-" makes
# it optional so a not-yet-populated env file does not fail the unit.
EnvironmentFile=-{{.EnvFile}}
# Bridges detach via Setsid into init.scope (sysproc_linux.go) and survive this oneshot
# exiting; KillMode=process is belt-and-suspenders so systemd never reaps them.
KillMode=process
ExecStart={{.Binary}} bridge start --all --timeout {{.Timeout}}

[Install]
WantedBy={{.WantedBy}}
`

const systemdTimerTmpl = `[Unit]
Description=agent-hub fleet watchdog — periodic bridge start --all
Documentation=https://github.com/kishibashi3/agent-hub-control

[Timer]
# Boot-start: {{.BootSec}}s after boot (covers host sleep -> VM death -> wake).
OnBootSec={{.BootSec}}s
# Self-anchor: fire {{.ActiveSec}}s after ` + "`enable --now`" + ` even on an already-booted box.
OnActiveSec={{.ActiveSec}}s
# Watchdog: re-run start --all every {{.Interval}}s to revive individually-crashed bridges.
OnUnitActiveSec={{.Interval}}s
Unit=agent-hub-fleet.service
Persistent=false

[Install]
WantedBy=timers.target
`

const launchdPlistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>
  <!-- Source the secret-free env file (secrets stay there, mode 0600), then exec the
       idempotent fleet start. set -a exports every sourced KEY=value to the child. -->
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>-c</string>
    <string>set -a; [ -f "{{.EnvFile}}" ] &amp;&amp; . "{{.EnvFile}}"; exec "{{.Binary}}" bridge start --all --timeout {{.Timeout}}</string>
  </array>
  <!-- RunAtLoad = boot/login start. StartInterval = watchdog. KeepAlive is deliberately
       OMITTED: the fleet start is oneshot and exits; KeepAlive would relaunch it in a
       tight loop. Bridges detach (setsid) and survive, mirroring the systemd oneshot. -->
  <key>RunAtLoad</key>
  <true/>
  <key>StartInterval</key>
  <integer>{{.Interval}}</integer>
  <key>WorkingDirectory</key>
  <string>{{.Home}}</string>
  <key>StandardOutPath</key>
  <string>{{.LogDir}}/fleet.out.log</string>
  <key>StandardErrorPath</key>
  <string>{{.LogDir}}/fleet.err.log</string>
</dict>
</plist>
`

const envScaffoldTmpl = `# agent-hub fleet environment — read by the boot-start/watchdog unit.
# Secrets live HERE (mode 0600), never in the unit template.
# Syntax: KEY=value per line (systemd EnvironmentFile / POSIX sh compatible —
# no 'export', no shell expansion). Lines starting with '#' are comments.
#
# PATH is captured at install time so bridge binaries (node/mise shims, agenthubctl)
# resolve without a login shell. Adjust if your toolchain moves.
PATH={{.PATH}}
#
# Fill in the secrets/endpoints your bridges need; the next watchdog tick picks them up:
# GITHUB_PAT=ghp_xxxxxxxxxxxxxxxx
# AGENT_HUB_URL=https://your-hub.example.com/mcp
# AGENT_HUB_TENANT=your-tenant
`

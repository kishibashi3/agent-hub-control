package fleet

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// testConfig returns a Config with deliberately non-local, sentinel values so tests can
// assert the renderers substitute them and never leak machine-specific paths.
func testConfig(scope Scope, init string) *Config {
	return &Config{
		OS:        map[string]string{initSystemd: "linux", initLaunchd: "darwin"}[init],
		Init:      init,
		Scope:     scope,
		User:      "testuser",
		UID:       4242,
		GID:       4242,
		Home:      "/home/testuser",
		Binary:    "/opt/bin/agenthubctl",
		EnvFile:   "/home/testuser/.agent-hub/fleet.env",
		PATH:      "/opt/bin:/usr/bin",
		Timeout:   40,
		BootSec:   30,
		ActiveSec: 30,
		Interval:  180,
	}
}

func TestRenderSystemdServiceSystemScope(t *testing.T) {
	c := testConfig(ScopeSystem, initSystemd)
	out, err := renderSystemdService(c)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, []string{
		"Type=oneshot",
		"User=testuser",
		"WorkingDirectory=/home/testuser",
		"EnvironmentFile=-/home/testuser/.agent-hub/fleet.env",
		"KillMode=process",
		"ExecStart=/opt/bin/agenthubctl bridge start --all --timeout 40",
		"WantedBy=multi-user.target",
	})
}

func TestRenderSystemdServiceUserScope(t *testing.T) {
	c := testConfig(ScopeUser, initSystemd)
	out, err := renderSystemdService(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "User=") {
		t.Errorf("user-scope service must NOT contain a User= directive:\n%s", out)
	}
	mustContain(t, out, []string{"WantedBy=default.target"})
}

func TestRenderSystemdTimer(t *testing.T) {
	c := testConfig(ScopeSystem, initSystemd)
	out, err := renderSystemdTimer(c)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, []string{
		"OnBootSec=30s",
		"OnActiveSec=30s",
		"OnUnitActiveSec=180s",
		"Unit=agent-hub-fleet.service",
		"WantedBy=timers.target",
	})
}

func TestRenderSystemdTimerCustomInterval(t *testing.T) {
	c := testConfig(ScopeSystem, initSystemd)
	c.Interval = 300
	out, err := renderSystemdTimer(c)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, []string{"OnUnitActiveSec=300s"})
}

func TestRenderLaunchdPlist(t *testing.T) {
	c := testConfig(ScopeUser, initLaunchd)
	out, err := renderLaunchdPlist(c)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, []string{
		"<string>com.agent-hub.fleet</string>",
		"<key>RunAtLoad</key>",
		"<key>StartInterval</key>",
		"<integer>180</integer>",
		`exec "/opt/bin/agenthubctl" bridge start --all --timeout 40`,
		"/home/testuser/.agent-hub/fleet.env",
	})
	// KeepAlive would relaunch the oneshot in a tight loop — the key must be absent
	// (the rationale comment may mention the word, so match the actual plist key).
	if strings.Contains(out, "<key>KeepAlive</key>") {
		t.Errorf("launchd plist must NOT set the KeepAlive key:\n%s", out)
	}
}

// TestLaunchdPlistIsWellFormedXML validates the generated plist parses as XML. macOS
// hardware isn't available in CI, so this (plus `plutil -lint` on a Mac) is the validity
// check for the launchd path.
func TestLaunchdPlistIsWellFormedXML(t *testing.T) {
	c := testConfig(ScopeUser, initLaunchd)
	out, err := renderLaunchdPlist(c)
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, out)
		}
	}
}

func TestRenderEnvScaffold(t *testing.T) {
	c := testConfig(ScopeSystem, initSystemd)
	out := renderEnvScaffold(c)
	mustContain(t, out, []string{
		"PATH=/opt/bin:/usr/bin",
		"# GITHUB_PAT=",
		"mode 0600",
	})
}

// TestNoHardcodedMachinePaths is the completion-condition guard: rendered output must
// contain only the resolved Config values, never the developer's machine path.
func TestNoHardcodedMachinePaths(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*Config) (string, error)
		c    *Config
	}{
		{"systemd-service-system", renderSystemdService, testConfig(ScopeSystem, initSystemd)},
		{"systemd-service-user", renderSystemdService, testConfig(ScopeUser, initSystemd)},
		{"systemd-timer", renderSystemdTimer, testConfig(ScopeSystem, initSystemd)},
		{"launchd-plist", renderLaunchdPlist, testConfig(ScopeUser, initLaunchd)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.fn(tc.c)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"/home/kishibashi3", "ope-ultp1635", "kishibashi3/.bashrc"} {
				if strings.Contains(out, forbidden) {
					t.Errorf("rendered output leaks machine-specific value %q:\n%s", forbidden, out)
				}
			}
		})
	}
}

func mustContain(t *testing.T, out string, subs []string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("expected output to contain %q, got:\n%s", s, out)
		}
	}
}

package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// asEuid returns an euidOverride (the issue #52 injection point) so cross-user (root)
// teardown branches can be exercised host-independently on a specific Config.
func asEuid(uid int) func() int { return func() int { return uid } }

// TestSystemctlInvocationCrossUserWrapsSudo: a root-driven user-scope teardown of another
// user's units must reach that user's session bus (issue #44), not root's empty --user bus.
func TestSystemctlInvocationCrossUserWrapsSudo(t *testing.T) {
	c := &Config{euidOverride: asEuid(0), Scope: ScopeUser, User: "alice", UID: 1001}
	name, argv := c.systemctlInvocation("disable", "--now", serviceName+".timer")
	if name != "sudo" {
		t.Fatalf("exec name = %q, want sudo", name)
	}
	want := []string{
		"-u", "alice", "env",
		"XDG_RUNTIME_DIR=/run/user/1001",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus",
		"systemctl", "--user", "disable", "--now", serviceName + ".timer",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("cross-user argv =\n  %v\nwant\n  %v", argv, want)
	}
}

// TestSystemctlInvocationSameUserPlain: a user tearing down their own units uses plain
// `systemctl --user` (their own bus is correct).
func TestSystemctlInvocationSameUserPlain(t *testing.T) {
	c := &Config{euidOverride: asEuid(1000), Scope: ScopeUser, User: "alice", UID: 1000}
	name, argv := c.systemctlInvocation("daemon-reload")
	if name != "systemctl" || !reflect.DeepEqual(argv, []string{"--user", "daemon-reload"}) {
		t.Errorf("same-user invocation = %q %v, want systemctl [--user daemon-reload]", name, argv)
	}
}

// TestSystemctlInvocationSystemScopePlain: system scope (even as root) uses plain systemctl,
// no --user, no sudo wrapping — the system bus is correct.
func TestSystemctlInvocationSystemScopePlain(t *testing.T) {
	c := &Config{euidOverride: asEuid(0), Scope: ScopeSystem, UID: 0}
	name, argv := c.systemctlInvocation("daemon-reload")
	if name != "systemctl" || !reflect.DeepEqual(argv, []string{"daemon-reload"}) {
		t.Errorf("system-scope invocation = %q %v, want systemctl [daemon-reload]", name, argv)
	}
}

// TestSystemdUnitDirForCrossUserIgnoresProcessXDG: under sudo, the process's own
// XDG_CONFIG_HOME (root's) must not be applied to the target user's unit dir (issue #44 #1).
func TestSystemdUnitDirForCrossUserIgnoresProcessXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/root/.config") // root's — must be ignored for the target
	c := &Config{euidOverride: asEuid(0), Scope: ScopeSystem, Home: "/home/alice", UID: 1001}
	got := c.systemdUnitDirFor(ScopeUser)
	want := filepath.Join("/home/alice", ".config", "systemd", "user")
	if got != want {
		t.Errorf("cross-user user unit dir = %q, want %q (process XDG must be ignored)", got, want)
	}
}

// TestStopSystemdUnitsWarnsWhenInvokingUserUnknown: root tearing down a user-scope install
// without a known SUDO_USER can't reach the real user's bus — must warn, not silently skip.
func TestStopSystemdUnitsWarnsWhenInvokingUserUnknown(t *testing.T) {
	c := &Config{euidOverride: asEuid(0), Scope: ScopeUser, UID: 0}
	if warn := c.stopSystemdUnits(); warn == "" {
		t.Error("expected warning when invoking user is unknown, got none")
	}
}

// TestStopSystemdUnitsWarnsWhenNoUserSession: cross-user teardown with no /run/user/<uid>
// means no live user manager — files are removed but the operator is told it self-heals.
func TestStopSystemdUnitsWarnsWhenNoUserSession(t *testing.T) {
	c := &Config{euidOverride: asEuid(0), Scope: ScopeUser, User: "ghost", UID: 2147480000} // no such runtime dir
	if warn := c.stopSystemdUnits(); warn == "" {
		t.Error("expected warning when target user has no active session, got none")
	}
}

// TestWriteEnvScaffoldDoesNotClobber verifies an existing env file (which may hold
// secrets) is never overwritten by a repeat install.
func TestWriteEnvScaffoldDoesNotClobber(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".agent-hub", "fleet.env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "GITHUB_PAT=ghp_supersecret\n"
	if err := os.WriteFile(envFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Config{Scope: ScopeUser, EnvFile: envFile}
	if err := c.writeEnvScaffold("PATH=/should/not/appear\n"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != secret {
		t.Errorf("existing env file was clobbered:\n got: %q\nwant: %q", got, secret)
	}
}

// TestWriteEnvScaffoldCreatesWith0600 verifies the scaffold is created with secret-safe
// permissions when absent.
func TestWriteEnvScaffoldCreatesWith0600(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".agent-hub", "fleet.env")

	c := &Config{Scope: ScopeUser, EnvFile: envFile}
	if err := c.writeEnvScaffold("PATH=/opt/bin\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("env scaffold perm = %o, want 0600", perm)
	}
}

// TestWriteEnvScaffoldSystemScopeChownViaEuidSeam: the system-scope chown hand-off goes through
// c.geteuid() (issue #69), so injecting root makes the scaffold take the chown branch and a
// non-root injection skips it. GID is a supplementary group of the test process (chown to
// own uid + a member group needs no privilege), so the file's gid shows whether the branch ran.
func TestWriteEnvScaffoldSystemScopeChownViaEuidSeam(t *testing.T) {
	primary := os.Getgid()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	target := -1
	for _, g := range groups {
		if g != primary {
			target = g
			break
		}
	}
	if target < 0 {
		t.Skipf("process has no supplementary group besides primary gid %d", primary)
	}

	for _, tc := range []struct {
		name    string
		euid    int
		wantGID int
	}{
		{"root takes chown branch", 0, target},
		// Without chown a new file takes the creator's primary gid; this relies on Linux
		// semantics and on the temp parent dir not being setgid (which would inherit its gid).
		{"non-root skips chown branch", 1000, primary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envFile := filepath.Join(t.TempDir(), "agent-hub", "fleet.env")
			c := &Config{euidOverride: asEuid(tc.euid), Scope: ScopeSystem, EnvFile: envFile, UID: os.Getuid(), GID: target}
			if err := c.writeEnvScaffold("PATH=/opt/bin\n"); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{filepath.Dir(envFile), envFile} {
				info, err := os.Stat(p)
				if err != nil {
					t.Fatal(err)
				}
				if gid := int(info.Sys().(*syscall.Stat_t).Gid); gid != tc.wantGID {
					t.Errorf("%s gid = %d, want %d", p, gid, tc.wantGID)
				}
			}
		})
	}
}

// TestEnvFilePermWarning: an existing env file readable by group/other gets a warning that
// names the file and the chmod fix; a 0600 (or 0400) file and a missing file stay silent
// (issue #51).
func TestEnvFilePermWarning(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		perm os.FileMode
		warn bool
	}{
		{"0644-warns", 0o644, true},
		{"0640-warns", 0o640, true},
		{"0604-warns", 0o604, true},
		{"0600-silent", 0o600, false},
		{"0400-silent", 0o400, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name+".env")
			if err := os.WriteFile(p, []byte("GITHUB_PAT=x\n"), tc.perm); err != nil {
				t.Fatal(err)
			}
			// WriteFile honours umask; force the exact mode under test.
			if err := os.Chmod(p, tc.perm); err != nil {
				t.Fatal(err)
			}
			w := envFilePermWarning(p)
			if tc.warn && (w == "" || !strings.Contains(w, "chmod 600 "+p)) {
				t.Errorf("perm %04o: expected warning with chmod hint, got %q", tc.perm, w)
			}
			if !tc.warn && w != "" {
				t.Errorf("perm %04o: expected no warning, got %q", tc.perm, w)
			}
		})
	}
	if w := envFilePermWarning(filepath.Join(dir, "missing.env")); w != "" {
		t.Errorf("missing file: expected no warning, got %q", w)
	}
}

// TestWriteEnvScaffoldDoesNotChmodExisting: a loose existing env file is warned about but
// never chmod'ed or rewritten by install (issue #51: warning only, no auto-fix).
func TestWriteEnvScaffoldDoesNotChmodExisting(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "fleet.env")
	if err := os.WriteFile(envFile, []byte("GITHUB_PAT=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envFile, 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Config{Scope: ScopeUser, EnvFile: envFile}
	if err := c.writeEnvScaffold("# scaffold\n"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("install changed mode of existing env file to %04o (must not auto-chmod)", got)
	}
	b, _ := os.ReadFile(envFile)
	if string(b) != "GITHUB_PAT=secret\n" {
		t.Errorf("install rewrote existing env file: %q", b)
	}
}

// TestInstallSystemdDryRunWritesNothing verifies --dry-run touches no files and runs no
// commands (it returns before any write/exec).
func TestInstallSystemdDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	c := &Config{
		OS: "linux", Init: initSystemd, Scope: ScopeUser,
		User: "testuser", Home: home,
		Binary: "/opt/bin/agenthubctl", EnvFile: filepath.Join(home, ".agent-hub", "fleet.env"),
		Timeout: 40, BootSec: 30, ActiveSec: 30, Interval: 180,
	}
	if err := c.installSystemd(true); err != nil {
		t.Fatal(err)
	}
	// No unit dir, no env file should have been created.
	if _, err := os.Stat(c.EnvFile); !os.IsNotExist(err) {
		t.Errorf("dry-run created env file %s", c.EnvFile)
	}
	if _, err := os.Stat(c.systemdUnitDir()); !os.IsNotExist(err) {
		t.Errorf("dry-run created unit dir %s", c.systemdUnitDir())
	}
}

func TestSystemdUnitDir(t *testing.T) {
	sys := &Config{Scope: ScopeSystem}
	if got := sys.systemdUnitDir(); got != "/etc/systemd/system" {
		t.Errorf("system unit dir = %q, want /etc/systemd/system", got)
	}

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	usr := &Config{Scope: ScopeUser, Home: home}
	want := filepath.Join(home, ".config", "systemd", "user")
	if got := usr.systemdUnitDir(); got != want {
		t.Errorf("user unit dir = %q, want %q", got, want)
	}
}

func TestSystemctlArgsUserScopePrefix(t *testing.T) {
	usr := &Config{Scope: ScopeUser}
	got := usr.systemctlArgs("daemon-reload")
	if len(got) != 2 || got[0] != "--user" || got[1] != "daemon-reload" {
		t.Errorf("user systemctlArgs = %v, want [--user daemon-reload]", got)
	}
	sys := &Config{Scope: ScopeSystem}
	if got := sys.systemctlArgs("daemon-reload"); len(got) != 1 || got[0] != "daemon-reload" {
		t.Errorf("system systemctlArgs = %v, want [daemon-reload]", got)
	}
}

func TestResolveRejectsConflictingScopeFlags(t *testing.T) {
	if _, err := Resolve(ResolveOptions{ForceSystem: true, ForceUser: true}); err == nil {
		t.Error("expected error for --system + --user, got nil")
	}
}

func TestResolveRelativeBinaryRejected(t *testing.T) {
	if runtime.GOOS != "linux" || !systemdAvailable() {
		t.Skip("requires linux+systemd")
	}
	if _, err := Resolve(ResolveOptions{Binary: "agenthubctl", ForceUser: true}); err == nil {
		t.Error("expected error for relative --binary, got nil")
	}
}

func TestOtherScope(t *testing.T) {
	if got := (&Config{Scope: ScopeSystem}).otherScope(); got != ScopeUser {
		t.Errorf("otherScope(system) = %q, want user", got)
	}
	if got := (&Config{Scope: ScopeUser}).otherScope(); got != ScopeSystem {
		t.Errorf("otherScope(user) = %q, want system", got)
	}
}

// seedUserScopeTimer writes a fake user-scope timer (+service) into home and returns the
// .timer path. Used to simulate an orphaned prior --user install.
func seedUserScopeTimer(t *testing.T, home string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmr := filepath.Join(dir, serviceName+".timer")
	for _, name := range []string{serviceName + ".service", serviceName + ".timer"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return tmr
}

// TestCrossScopeGuardNoOrphanPasses: with no opposite-scope units, the guard is a no-op.
// Target system scope so the probed opposite scope is the per-home user dir (an empty
// tempdir) rather than the host's real /etc/systemd/system, keeping the test hermetic.
func TestCrossScopeGuardNoOrphanPasses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	c := &Config{Scope: ScopeSystem, Home: home}
	if err := c.crossScopeGuard(); err != nil {
		t.Errorf("guard with no orphan returned error: %v", err)
	}
}

// TestCrossScopeGuardAbortsWithoutForce: a user-scope orphan blocks a system-scope install
// when --force is absent, and the orphan files are left untouched.
func TestCrossScopeGuardAbortsWithoutForce(t *testing.T) {
	home := t.TempDir()
	tmr := seedUserScopeTimer(t, home)

	c := &Config{Scope: ScopeSystem, Home: home, Force: false}
	err := c.crossScopeGuard()
	if err == nil {
		t.Fatal("expected abort error for cross-scope orphan, got nil")
	}
	if _, statErr := os.Stat(tmr); statErr != nil {
		t.Errorf("abort path removed the orphan timer (should be untouched): %v", statErr)
	}
}

// TestCrossScopeGuardForceRemovesOtherScope: with --force, the user-scope orphan is removed
// before the system-scope install proceeds. (systemctl calls are best-effort/quiet, so this
// runs even where systemd is absent.)
func TestCrossScopeGuardForceRemovesOtherScope(t *testing.T) {
	home := t.TempDir()
	tmr := seedUserScopeTimer(t, home)
	calls := resetSystemctlCalls(t)

	c := &Config{
		Scope: ScopeSystem, Home: home, Force: true,
		EnvFile: filepath.Join(home, ".agent-hub", "fleet.env"),
	}
	if err := c.crossScopeGuard(); err != nil {
		t.Fatalf("--force guard returned error: %v", err)
	}
	if _, statErr := os.Stat(tmr); !os.IsNotExist(statErr) {
		t.Errorf("--force did not remove the orphan timer %s (err=%v)", tmr, statErr)
	}
	// The teardown must target the *user* scope (issue #55 recorder: intent is asserted, the
	// real binary is never reached).
	want := "--user disable --now " + serviceName + ".timer"
	if got := calls(); !strings.Contains(strings.Join(got, "\n"), want) {
		t.Errorf("expected recorded systemctl call %q, got %q", want, got)
	}
}

// TestResolveBakesAbsoluteBinaryAndDefaults checks the happy path on a systemd host.
func TestResolveBakesAbsoluteBinaryAndDefaults(t *testing.T) {
	if runtime.GOOS != "linux" || !systemdAvailable() {
		t.Skip("requires linux+systemd")
	}
	c, err := Resolve(ResolveOptions{Binary: "/opt/bin/agenthubctl", ForceUser: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Binary != "/opt/bin/agenthubctl" {
		t.Errorf("binary = %q", c.Binary)
	}
	if c.Init != initSystemd || c.Scope != ScopeUser {
		t.Errorf("init/scope = %q/%q, want systemd/user", c.Init, c.Scope)
	}
	if c.Timeout != defaultTimeout || c.Interval != defaultInterval {
		t.Errorf("defaults not applied: timeout=%d interval=%d", c.Timeout, c.Interval)
	}
	if !filepath.IsAbs(c.EnvFile) {
		t.Errorf("env file not absolute: %q", c.EnvFile)
	}
}

// seedSystemScopeUnits writes fake agent-hub-fleet.{service,timer} into a tempdir that stands
// in for /etc/systemd/system, and returns a user-scope Config whose opposite-scope probe points
// at it (issue #52). No real /etc access, no root, no skip.
func seedSystemScopeUnits(t *testing.T) (c *Config, svc, tmr string) {
	t.Helper()
	fakeEtc := t.TempDir()
	for _, name := range []string{serviceName + ".service", serviceName + ".timer"} {
		if err := os.WriteFile(filepath.Join(fakeEtc, name), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	c = &Config{
		Scope:                 ScopeUser,
		Home:                  t.TempDir(),
		EnvFile:               filepath.Join(fakeEtc, "fleet.env"),
		systemUnitDirOverride: fakeEtc,
	}
	// Precondition: the override must actually redirect the opposite-scope probe at fakeEtc.
	// Without this, a non-functional override yields no orphans and the guard tests pass vacuously.
	if got := c.otherScopeSystemdUnits(); len(got) != 2 {
		t.Fatalf("seed precondition: otherScopeSystemdUnits() should see the 2 fake units in %s, got %v", fakeEtc, got)
	}
	return c, filepath.Join(fakeEtc, serviceName+".service"), filepath.Join(fakeEtc, serviceName+".timer")
}

func mustExist(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should be untouched: %v", p, err)
		}
	}
}

// TestCrossScopeGuardUserScopeSystemOrphanAborts: a user-scope install that finds a
// system-scope orphan aborts without --force, regardless of privilege, and leaves the
// orphan files in place.
func TestCrossScopeGuardUserScopeSystemOrphanAborts(t *testing.T) {
	for _, euid := range []int{0, 1000} {
		c, svc, tmr := seedSystemScopeUnits(t)
		c.Force = false
		c.euidOverride = func() int { return euid }
		err := c.crossScopeGuard()
		if err == nil {
			t.Fatalf("euid=%d: expected abort without --force when a system-scope orphan exists", euid)
		}
		if !strings.Contains(err.Error(), uninstallHint(ScopeSystem)) {
			t.Errorf("euid=%d: abort message should carry the removal hint %q, got: %v", euid, uninstallHint(ScopeSystem), err)
		}
		mustExist(t, svc, tmr)
	}
}

// TestCrossScopeGuardUserScopeSystemOrphanForceNonRootWarns: --force as non-root cannot remove
// /etc units, so the guard proceeds (nil) with a warning and the orphan is left untouched
// (issue #47: the sudo-less user watchdog must not be blocked behind root).
func TestCrossScopeGuardUserScopeSystemOrphanForceNonRootWarns(t *testing.T) {
	c, svc, tmr := seedSystemScopeUnits(t)
	c.Force = true
	c.euidOverride = func() int { return 1000 }
	if err := c.crossScopeGuard(); err != nil {
		t.Fatalf("--force as non-root should proceed with a warning, got: %v", err)
	}
	mustExist(t, svc, tmr)
}

// TestCrossScopeGuardUserScopeSystemOrphanForceRootRemoves: --force as root tears down the
// system-scope orphan before the user-scope install proceeds. systemctl calls inside
// uninstallSystemd are best-effort/quiet, so this runs where systemd is absent too.
func TestCrossScopeGuardUserScopeSystemOrphanForceRootRemoves(t *testing.T) {
	c, svc, tmr := seedSystemScopeUnits(t)
	calls := resetSystemctlCalls(t)
	c.Force = true
	c.euidOverride = func() int { return 0 }
	if err := c.crossScopeGuard(); err != nil {
		t.Fatalf("--force as root should remove the orphan and proceed, got: %v", err)
	}
	for _, p := range []string{svc, tmr} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("--force as root did not remove %s (err=%v)", p, err)
		}
	}
	got := calls()
	joined := strings.Join(got, "\n")
	if want := "disable --now " + serviceName + ".timer"; !strings.Contains(joined, want) {
		t.Errorf("expected recorded systemctl call %q, got %q", want, got)
	}
	if strings.Contains(joined, "--user") {
		t.Errorf("system-scope teardown must not pass --user, got %q", got)
	}
}

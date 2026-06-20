// systemd.go — install/uninstall/status for the systemd .service + .timer provider.
package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// systemdUnitDir is where the .service / .timer files are written for this scope.
func (c *Config) systemdUnitDir() string { return c.systemdUnitDirFor(c.Scope) }

// systemdUnitDirFor returns the unit dir for an arbitrary scope. It is parameterized (vs
// reading c.Scope) so install can probe the *opposite* scope for an orphaned prior install
// — the scope-change orphan case from issue #42.
func (c *Config) systemdUnitDirFor(scope Scope) string {
	if scope == ScopeSystem {
		return "/etc/systemd/system"
	}
	// XDG_CONFIG_HOME in our own environment describes the *current* process's user. When we
	// operate on another user's units as root (sudo), that value is root's (or unset) and
	// must not be applied to the target — fall back to <their HOME>/.config. A relocated
	// XDG_CONFIG_HOME for that user can't be seen here; warnCrossUserXDGBlindSpot surfaces it
	// (issue #44, reviewer suggestion #1).
	base := ""
	if !c.targetingOtherUser() {
		base = os.Getenv("XDG_CONFIG_HOME")
	}
	if base == "" {
		base = filepath.Join(c.Home, ".config")
	}
	return filepath.Join(base, "systemd", "user")
}

// geteuid is indirected so tests can exercise the root-only cross-user teardown branch.
var geteuid = os.Geteuid

// targetingOtherUser reports whether we run as root (sudo) but the resolved fleet target is
// a different, non-root user. In that case the process's own environment — XDG_CONFIG_HOME
// and the `--user` session bus — describes root, not the user whose units we must read and
// whose *live* timer we must stop (issue #44).
func (c *Config) targetingOtherUser() bool {
	return geteuid() == 0 && c.UID != 0
}

// otherScope is the scope opposite to c.Scope.
func (c *Config) otherScope() Scope {
	if c.Scope == ScopeSystem {
		return ScopeUser
	}
	return ScopeSystem
}

// withScope returns a shallow copy of c with a different scope, so the existing
// scope-aware uninstall logic can be reused to tear down the opposite scope.
func (c *Config) withScope(s Scope) *Config {
	cp := *c
	cp.Scope = s
	return &cp
}

// otherScopeSystemdUnits returns the service/timer file paths that exist in the opposite
// scope's unit dir (empty if none). A non-empty result is the orphan risk: re-installing
// here without removing them leaves two watchdog timers running.
func (c *Config) otherScopeSystemdUnits() []string {
	dir := c.systemdUnitDirFor(c.otherScope())
	var found []string
	for _, name := range []string{serviceName + ".service", serviceName + ".timer"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	return found
}

// uninstallHint is the command a user would run to remove an install of the given scope.
func uninstallHint(scope Scope) string {
	if scope == ScopeSystem {
		return "sudo agenthubctl fleet uninstall --system"
	}
	return "agenthubctl fleet uninstall --user"
}

// crossScopeGuard handles the issue #42 orphan case: an existing install in the *opposite*
// scope. Default is fail-fast — abort with the exact command to remove the orphan — because
// silently tearing down the other scope touches a live watchdog/boot path without consent.
// With --force we uninstall the other scope first so exactly one watchdog survives.
// Same-scope reinstalls are idempotent (overwrite in place) and never reach here.
func (c *Config) crossScopeGuard() error {
	orphans := c.otherScopeSystemdUnits()
	if len(orphans) == 0 {
		return nil
	}
	other := c.otherScope()
	if !c.Force {
		return fmt.Errorf(
			"existing %s-scope install found (%s) while installing %s scope.\n"+
				"Re-installing here would orphan the %s-scope timer and run two watchdogs.\n"+
				"Remove the old scope first, then re-run install:\n"+
				"  %s\n"+
				"Or pass --force to have install remove the %s-scope units automatically.",
			other, strings.Join(orphans, ", "), c.Scope, other, uninstallHint(other), other)
	}
	fmt.Printf("--force: removing existing %s-scope install before installing %s scope...\n", other, c.Scope)
	if err := c.withScope(other).uninstallSystemd(); err != nil {
		return fmt.Errorf("clean up %s-scope install (try: %s): %w", other, uninstallHint(other), err)
	}
	return nil
}

// warnCrossUserXDGBlindSpot surfaces reviewer suggestion #1 (issue #44): under sudo we can't
// read the *target* user's XDG_CONFIG_HOME, so we probe <their HOME>/.config for user-scope
// units. Warn that a relocated XDG_CONFIG_HOME for that user could hide a user-scope orphan
// from otherScopeSystemdUnits — a detection blind spot, surfaced rather than left silent.
func (c *Config) warnCrossUserXDGBlindSpot() {
	if !c.targetingOtherUser() {
		return
	}
	fmt.Fprintf(os.Stderr,
		"note: running as root for user %s — probing %s for user-scope units.\n"+
			"  If %s sets a non-standard XDG_CONFIG_HOME, a user-scope orphan there may go undetected.\n\n",
		c.User, c.systemdUnitDirFor(ScopeUser), c.User)
}

// systemctlArgs prefixes --user for user-scope invocations.
func (c *Config) systemctlArgs(args ...string) []string {
	if c.Scope == ScopeUser {
		return append([]string{"--user"}, args...)
	}
	return args
}

func (c *Config) installSystemd(dryRun bool) error {
	c.warnCrossUserXDGBlindSpot()
	svc, err := renderSystemdService(c)
	if err != nil {
		return fmt.Errorf("render service: %w", err)
	}
	tmr, err := renderSystemdTimer(c)
	if err != nil {
		return fmt.Errorf("render timer: %w", err)
	}
	env := renderEnvScaffold(c)

	dir := c.systemdUnitDir()
	svcPath := filepath.Join(dir, serviceName+".service")
	tmrPath := filepath.Join(dir, serviceName+".timer")

	if dryRun {
		actions := []string{
			"systemctl " + strings.Join(c.systemctlArgs("daemon-reload"), " "),
			"systemctl " + strings.Join(c.systemctlArgs("enable", "--now", serviceName+".timer"), " "),
		}
		if orphans := c.otherScopeSystemdUnits(); len(orphans) > 0 {
			other := c.otherScope()
			note := fmt.Sprintf("NOTE: %s-scope install present (%s) — install would ABORT (pass --force to remove it: %s)",
				other, strings.Join(orphans, ", "), uninstallHint(other))
			if c.Force {
				note = fmt.Sprintf("NOTE: --force will first run `%s` to remove the %s-scope install (%s)",
					uninstallHint(other), other, strings.Join(orphans, ", "))
			}
			actions = append([]string{note}, actions...)
		}
		printDryRun(c, []genFile{
			{svcPath, svc},
			{tmrPath, tmr},
			{c.EnvFile + "  (scaffold — written only if absent)", env},
		}, actions)
		return nil
	}

	// 0. orphan guard: a prior install in the *other* scope would leave two watchdogs.
	if err := c.crossScopeGuard(); err != nil {
		return err
	}

	// 1. env scaffold (never clobbers an existing file that may hold secrets)
	if err := c.writeEnvScaffold(env); err != nil {
		return err
	}

	// 2. unit files
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(svcPath, []byte(svc), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", svcPath, err)
	}
	if err := os.WriteFile(tmrPath, []byte(tmr), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmrPath, err)
	}

	// 3. enable + start the timer
	if err := c.runSystemctl("daemon-reload"); err != nil {
		return err
	}
	if err := c.runSystemctl("enable", "--now", serviceName+".timer"); err != nil {
		return err
	}

	fmt.Printf("✓ installed %s timer (%s scope)\n", serviceName, c.Scope)
	fmt.Printf("  service:   %s\n", svcPath)
	fmt.Printf("  timer:     %s\n", tmrPath)
	fmt.Printf("  env file:  %s\n", c.EnvFile)
	fmt.Printf("  ExecStart: %s bridge start --all --timeout %d\n", c.Binary, c.Timeout)
	if c.Scope == ScopeUser {
		fmt.Println()
		fmt.Println("NOTE: user-scope units only boot-start when lingering is enabled. To survive")
		fmt.Println("reboots without an interactive login, run:")
		fmt.Printf("  loginctl enable-linger %s\n", c.User)
	}
	fmt.Println()
	fmt.Printf("Populate secrets/endpoints in %s (GITHUB_PAT, AGENT_HUB_URL, ...).\n", c.EnvFile)
	return nil
}

// stopSystemdUnits stops + disables the live timer/service for this scope. It returns a
// human-readable warning when the live stop could NOT be guaranteed — so the caller can tell
// the operator that removing the unit files alone may leave a user-scope timer running until
// the target user's next daemon-reload / login, rather than silently breaking the "exactly
// one watchdog" invariant (issue #44). All systemctl calls are best-effort; the caller
// removes the unit files regardless.
func (c *Config) stopSystemdUnits() string {
	// Root-driven user-scope teardown where we can't identify the invoking user (SUDO_USER
	// unset/root): `systemctl --user` would bind to root's own bus, never the real user's, so
	// removing files won't stop their live timer.
	if c.Scope == ScopeUser && geteuid() == 0 && c.UID == 0 {
		return fmt.Sprintf("could not identify the invoking user (set SUDO_USER) — removed unit "+
			"files but a live user-scope %s.timer may still run; stop it with "+
			"`systemctl --user disable --now %s.timer` as that user (self-heals on next login)",
			serviceName, serviceName)
	}

	// Cross-user user scope (root via sudo): reach the invoking user's session bus so the
	// *live* timer is stopped, not just the unit file deleted.
	if c.Scope == ScopeUser && c.targetingOtherUser() {
		runtimeDir := fmt.Sprintf("/run/user/%d", c.UID)
		if _, err := os.Stat(runtimeDir); err != nil {
			// No live user manager (no runtime dir) → there is no live timer to stop; the file
			// removal below is sufficient and a future login self-heals.
			return fmt.Sprintf("user %s has no active login session (%s missing) — removed unit "+
				"files; any user-scope %s.timer self-heals on their next login",
				c.User, runtimeDir, serviceName)
		}
		if err := c.runSystemctlQuiet("disable", "--now", serviceName+".timer"); err != nil {
			return fmt.Sprintf("failed to stop the live user-scope %s.timer for %s via their session "+
				"bus (%v) — removed unit files; run `systemctl --user disable --now %s.timer` as %s "+
				"if it persists", serviceName, c.User, err, serviceName, c.User)
		}
		if err := c.runSystemctlQuiet("stop", serviceName+".service"); err != nil {
			// Timer is already disabled (no re-trigger), but surface the service-stop failure
			// rather than silent-skip it — an in-flight run may still be active (issue #44).
			return fmt.Sprintf("stopped the user-scope %s.timer for %s but could not stop a running "+
				"%s.service (%v) — removed unit files; run `systemctl --user stop %s.service` as %s "+
				"if it persists", serviceName, c.User, serviceName, err, serviceName, c.User)
		}
		return ""
	}

	// System scope, or a user removing their own units: the default bus is correct.
	_ = c.runSystemctlQuiet("disable", "--now", serviceName+".timer")
	_ = c.runSystemctlQuiet("stop", serviceName+".service")
	return ""
}

func (c *Config) uninstallSystemd() error {
	// Stop + disable the live units first, then remove the files. A non-empty warning means we
	// could not reach the right session manager — surfacing that on-disk removal alone leaves a
	// live user timer until the next daemon-reload / re-login (issue #44).
	if warn := c.stopSystemdUnits(); warn != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warn)
	}

	dir := c.systemdUnitDir()
	var removed []string
	for _, name := range []string{serviceName + ".service", serviceName + ".timer"} {
		p := filepath.Join(dir, name)
		switch err := os.Remove(p); {
		case err == nil:
			removed = append(removed, p)
		case os.IsNotExist(err):
			// nothing to do
		default:
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	_ = c.runSystemctlQuiet("daemon-reload")

	if len(removed) == 0 {
		fmt.Printf("nothing to remove (no %s units in %s)\n", serviceName, dir)
	} else {
		fmt.Printf("✓ removed %d unit file(s):\n", len(removed))
		for _, p := range removed {
			fmt.Printf("  %s\n", p)
		}
	}
	fmt.Printf("env file left in place (may hold secrets): %s\n", c.EnvFile)
	return nil
}

func (c *Config) statusSystemd() error {
	dir := c.systemdUnitDir()
	svcPath := filepath.Join(dir, serviceName+".service")
	tmrPath := filepath.Join(dir, serviceName+".timer")

	fmt.Printf("scope:     %s (init=systemd)\n", c.Scope)
	fmt.Printf("service:   %s%s\n", svcPath, existsMark(svcPath))
	fmt.Printf("timer:     %s%s\n", tmrPath, existsMark(tmrPath))
	fmt.Printf("env file:  %s%s\n", c.EnvFile, existsMark(c.EnvFile))
	fmt.Println()
	fmt.Printf("timer is-enabled: %s\n", c.systemctlQuery("is-enabled", serviceName+".timer"))
	fmt.Printf("timer is-active:  %s\n", c.systemctlQuery("is-active", serviceName+".timer"))
	fmt.Println()

	cmd := c.systemctlCmd("list-timers", serviceName+".timer", "--no-pager")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	return nil
}

// writeEnvScaffold writes the env template ONLY if the file does not already exist, so
// repeated installs never overwrite user-supplied secrets. For a root-driven system
// install targeting another user, ownership is handed to that user.
func (c *Config) writeEnvScaffold(content string) error {
	if _, err := os.Stat(c.EnvFile); err == nil {
		return nil // already present — leave user secrets untouched
	}
	dir := filepath.Dir(c.EnvFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(c.EnvFile, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write env scaffold: %w", err)
	}
	if c.Scope == ScopeSystem && os.Geteuid() == 0 {
		_ = os.Chown(dir, c.UID, c.GID)
		_ = os.Chown(c.EnvFile, c.UID, c.GID)
	}
	return nil
}

// systemctlInvocation returns the executable + argv for a systemctl call in this scope. For
// a user-scope target owned by another user while we run as root (the sudo … --force teardown
// of a user orphan), it drops to that user and points systemctl at their session bus via
// XDG_RUNTIME_DIR, so `systemctl --user disable --now` stops the invoking user's *live* timer
// instead of binding to root's empty user bus. Pure (no exec) for testability (issue #44).
func (c *Config) systemctlInvocation(args ...string) (name string, argv []string) {
	full := c.systemctlArgs(args...)
	if c.Scope == ScopeUser && c.targetingOtherUser() {
		runtimeDir := fmt.Sprintf("/run/user/%d", c.UID)
		argv = []string{
			"-u", c.User, "env",
			"XDG_RUNTIME_DIR=" + runtimeDir,
			"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDir + "/bus",
			"systemctl",
		}
		return "sudo", append(argv, full...)
	}
	return "systemctl", full
}

func (c *Config) systemctlCmd(args ...string) *exec.Cmd {
	name, argv := c.systemctlInvocation(args...)
	return exec.Command(name, argv...)
}

func (c *Config) runSystemctl(args ...string) error {
	name, argv := c.systemctlInvocation(args...)
	cmd := exec.Command(name, argv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Report the actual invocation (incl. the sudo … --user wrapper for cross-user
		// teardown) so the error doubles as a reproduce command (issue #44).
		return fmt.Errorf("%s %s: %w", name, strings.Join(argv, " "), err)
	}
	return nil
}

func (c *Config) runSystemctlQuiet(args ...string) error {
	return c.systemctlCmd(args...).Run()
}

func (c *Config) systemctlQuery(args ...string) string {
	out, _ := c.systemctlCmd(args...).Output()
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "unknown"
	}
	return s
}

func existsMark(p string) string {
	if _, err := os.Stat(p); err == nil {
		return "  [present]"
	}
	return "  [missing]"
}

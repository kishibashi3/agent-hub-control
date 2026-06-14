// Package fleet implements `agenthubctl fleet install/uninstall/status` — a portable
// boot-start + watchdog for the bridge fleet. It detects the current user / OS / init
// system at runtime and generates secret-free unit files (systemd .service+.timer on
// Linux, launchd LaunchAgent .plist on macOS) that periodically run the idempotent
// `agenthubctl bridge start --all`. There are NO machine-specific hardcoded paths:
// user, HOME, and the agenthubctl binary are all resolved at install time.
//
// Design (ported from the proven operator/deploy/systemd baseline):
//   - Type=oneshot is safe because bridges detach via Setsid into init.scope
//     (sysproc_linux.go) and survive the unit exiting; KillMode=process is added as
//     belt-and-suspenders. launchd mirrors this with RunAtLoad+StartInterval and
//     deliberately NO KeepAlive (which would relaunch the oneshot in a tight loop).
//   - Secrets never live in the unit. They go in an EnvironmentFile (~/.agent-hub/
//     fleet.env, mode 0600) — replacing the old `source ~/.bashrc` dependency.
package fleet

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
)

const (
	// serviceName is the systemd unit base name (agent-hub-fleet.service / .timer).
	serviceName = "agent-hub-fleet"
	// launchdLabel is the launchd job label / plist base name.
	launchdLabel = "com.agent-hub.fleet"

	defaultTimeout   = 40  // per-bridge ready timeout (seconds)
	defaultBootSec   = 30  // OnBootSec — start 30s after boot (covers WSL2 VM revive)
	defaultActiveSec = 30  // OnActiveSec — self-anchor after `enable --now`
	defaultInterval  = 180 // OnUnitActiveSec / StartInterval — 3min watchdog
)

// Scope is system-level (boots without login) vs user-level (needs linger to boot-start).
type Scope string

const (
	ScopeSystem Scope = "system"
	ScopeUser   Scope = "user"
)

// initSystemd / initLaunchd are the supported init systems.
const (
	initSystemd = "systemd"
	initLaunchd = "launchd"
)

// Config is the fully-resolved install target. Every field is derived at runtime so the
// generated units carry no machine-specific assumptions.
type Config struct {
	OS        string // runtime.GOOS
	Init      string // initSystemd | initLaunchd
	Scope     Scope
	User      string // unix username (system scope: User= directive; otherwise informational)
	UID       int
	GID       int
	Home      string // target user's home directory
	Binary    string // absolute agenthubctl path baked into ExecStart
	EnvFile   string // EnvironmentFile path (secrets live here, not in the unit)
	PATH      string // install-time PATH, captured into the env scaffold
	Timeout   int
	BootSec   int
	ActiveSec int
	Interval  int
}

// ResolveOptions carries the install flags that influence resolution.
type ResolveOptions struct {
	ForceSystem bool
	ForceUser   bool
	Binary      string
	EnvFile     string
	Timeout     int
	Interval    int
}

// Resolve detects OS / init system / scope / user / paths and returns a ready Config.
func Resolve(opt ResolveOptions) (*Config, error) {
	if opt.ForceSystem && opt.ForceUser {
		return nil, fmt.Errorf("--system and --user are mutually exclusive")
	}

	c := &Config{
		OS:        runtime.GOOS,
		Timeout:   defaultTimeout,
		BootSec:   defaultBootSec,
		ActiveSec: defaultActiveSec,
		Interval:  defaultInterval,
		PATH:      os.Getenv("PATH"),
	}
	if opt.Timeout > 0 {
		c.Timeout = opt.Timeout
	}
	if opt.Interval > 0 {
		c.Interval = opt.Interval
	}

	// ── init system ───────────────────────────────────────────────────
	switch runtime.GOOS {
	case "linux":
		if !systemdAvailable() {
			return nil, fmt.Errorf("systemd not detected (no /run/systemd/system); only systemd-based Linux is supported")
		}
		c.Init = initSystemd
	case "darwin":
		c.Init = initLaunchd
	default:
		return nil, fmt.Errorf("unsupported OS %q (linux/systemd or macOS/launchd only)", runtime.GOOS)
	}

	// ── scope ─────────────────────────────────────────────────────────
	switch {
	case c.Init == initLaunchd:
		if opt.ForceSystem {
			return nil, fmt.Errorf("--system is not supported on macOS (LaunchAgent is user-scoped)")
		}
		c.Scope = ScopeUser
	case opt.ForceSystem:
		c.Scope = ScopeSystem
	case opt.ForceUser:
		c.Scope = ScopeUser
	case os.Geteuid() == 0:
		c.Scope = ScopeSystem
	default:
		c.Scope = ScopeUser
	}

	// ── user + home ───────────────────────────────────────────────────
	if err := c.resolveUser(); err != nil {
		return nil, err
	}

	// ── agenthubctl binary ────────────────────────────────────────────
	bin := opt.Binary
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve agenthubctl path: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		bin = exe
	}
	if !filepath.IsAbs(bin) {
		return nil, fmt.Errorf("binary path must be absolute: %q", bin)
	}
	c.Binary = bin

	// ── env file ──────────────────────────────────────────────────────
	if opt.EnvFile != "" {
		c.EnvFile = opt.EnvFile
	} else {
		c.EnvFile = filepath.Join(c.Home, ".agent-hub", "fleet.env")
	}

	return c, nil
}

// resolveUser fills User/UID/GID/Home for the chosen scope. For a system install invoked
// via sudo we target the *invoking* user's fleet (SUDO_USER), not root.
func (c *Config) resolveUser() error {
	if c.Scope == ScopeSystem {
		name := os.Getenv("SUDO_USER")
		if name == "" || name == "root" {
			u, err := user.Current()
			if err != nil {
				return fmt.Errorf("current user: %w", err)
			}
			name = u.Username
		}
		u, err := user.Lookup(name)
		if err != nil {
			return fmt.Errorf("lookup user %q: %w", name, err)
		}
		c.User = u.Username
		c.Home = u.HomeDir
		if c.UID, c.GID, err = parseIDs(u); err != nil {
			return err
		}
		return nil
	}

	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("current user: %w", err)
	}
	c.User = u.Username
	if c.UID, c.GID, err = parseIDs(u); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	c.Home = home
	return nil
}

// parseIDs converts a user's textual UID/GID to ints. We fail fast rather than swallow
// the error: a silent fallback to 0 would chown the 0600 secret env file to root:root
// (see systemd.go), exactly the kind of invisible privilege change install must never do.
func parseIDs(u *user.User) (uid, gid int, err error) {
	if uid, err = strconv.Atoi(u.Uid); err != nil {
		return 0, 0, fmt.Errorf("parse uid %q for user %q: %w", u.Uid, u.Username, err)
	}
	if gid, err = strconv.Atoi(u.Gid); err != nil {
		return 0, 0, fmt.Errorf("parse gid %q for user %q: %w", u.Gid, u.Username, err)
	}
	return uid, gid, nil
}

// systemdAvailable reports whether systemd is the running init (sd_booted equivalent).
func systemdAvailable() bool {
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

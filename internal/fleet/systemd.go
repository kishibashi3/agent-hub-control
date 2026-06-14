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
func (c *Config) systemdUnitDir() string {
	if c.Scope == ScopeSystem {
		return "/etc/systemd/system"
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(c.Home, ".config")
	}
	return filepath.Join(base, "systemd", "user")
}

// systemctlArgs prefixes --user for user-scope invocations.
func (c *Config) systemctlArgs(args ...string) []string {
	if c.Scope == ScopeUser {
		return append([]string{"--user"}, args...)
	}
	return args
}

func (c *Config) installSystemd(dryRun bool) error {
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
		printDryRun(c, []genFile{
			{svcPath, svc},
			{tmrPath, tmr},
			{c.EnvFile + "  (scaffold — written only if absent)", env},
		}, []string{
			"systemctl " + strings.Join(c.systemctlArgs("daemon-reload"), " "),
			"systemctl " + strings.Join(c.systemctlArgs("enable", "--now", serviceName+".timer"), " "),
		})
		return nil
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

func (c *Config) uninstallSystemd() error {
	// Best-effort teardown; ignore errors (units may already be gone).
	_ = c.runSystemctlQuiet("disable", "--now", serviceName+".timer")
	_ = c.runSystemctlQuiet("stop", serviceName+".service")

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

	cmd := exec.Command("systemctl", c.systemctlArgs("list-timers", serviceName+".timer", "--no-pager")...)
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

func (c *Config) runSystemctl(args ...string) error {
	full := c.systemctlArgs(args...)
	cmd := exec.Command("systemctl", full...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(full, " "), err)
	}
	return nil
}

func (c *Config) runSystemctlQuiet(args ...string) error {
	return exec.Command("systemctl", c.systemctlArgs(args...)...).Run()
}

func (c *Config) systemctlQuery(args ...string) string {
	out, _ := exec.Command("systemctl", c.systemctlArgs(args...)...).Output()
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

// launchd.go — install/uninstall/status for the macOS launchd LaunchAgent provider.
//
// No macOS hardware is available in CI, so this path is validated by (a) the render tests
// asserting plist structure / no hardcoded paths and (b) `plutil -lint` when run on a Mac.
// The load step prefers the modern `launchctl bootstrap gui/<uid>` verb and falls back to
// the legacy `launchctl load -w` for older macOS.
package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func (c *Config) launchdPlistPath() string {
	return filepath.Join(c.Home, "Library", "LaunchAgents", launchdLabel+".plist")
}

// installLaunchd has no cross-scope orphan guard (cf. issue #42 / installSystemd): launchd
// here is always a per-user LaunchAgent (gui/<uid>) — Resolve rejects --system on macOS —
// so the plist path is invariant and a re-install overwrites it in place. There is no
// second scope to orphan.
func (c *Config) installLaunchd(dryRun bool) error {
	plist, err := renderLaunchdPlist(c)
	if err != nil {
		return fmt.Errorf("render plist: %w", err)
	}
	env := renderEnvScaffold(c)
	plistPath := c.launchdPlistPath()

	if dryRun {
		printDryRun(c, []genFile{
			{plistPath, plist},
			{c.EnvFile + "  (scaffold — written only if absent)", env},
		}, []string{
			fmt.Sprintf("launchctl bootstrap gui/%d %s", c.UID, plistPath),
			fmt.Sprintf("(fallback) launchctl load -w %s", plistPath),
		})
		return nil
	}

	if err := c.writeEnvScaffold(env); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("mkdir LaunchAgents: %w", err)
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	if err := launchctlLoad(plistPath, c.UID); err != nil {
		fmt.Printf("✓ plist written: %s\n", plistPath)
		fmt.Printf("  but launchctl load failed: %v\n", err)
		fmt.Printf("  load manually: launchctl bootstrap gui/%d %s\n", c.UID, plistPath)
		fmt.Printf("env file: %s\n", c.EnvFile)
		return nil
	}

	fmt.Printf("✓ installed and loaded %s\n", launchdLabel)
	fmt.Printf("  plist:    %s\n", plistPath)
	fmt.Printf("  env file: %s\n", c.EnvFile)
	fmt.Printf("  ExecStart: %s bridge start --all --timeout %d\n", c.Binary, c.Timeout)
	fmt.Println()
	fmt.Printf("Populate secrets/endpoints in %s (GITHUB_PAT, AGENT_HUB_URL, ...).\n", c.EnvFile)
	return nil
}

func (c *Config) uninstallLaunchd() error {
	plistPath := c.launchdPlistPath()
	_ = launchctlUnload(plistPath, c.UID)

	switch err := os.Remove(plistPath); {
	case err == nil:
		fmt.Printf("✓ removed %s\n", plistPath)
	case os.IsNotExist(err):
		fmt.Printf("nothing to remove (no plist at %s)\n", plistPath)
	default:
		return fmt.Errorf("remove %s: %w", plistPath, err)
	}
	fmt.Printf("env file left in place (may hold secrets): %s\n", c.EnvFile)
	return nil
}

func (c *Config) statusLaunchd() error {
	plistPath := c.launchdPlistPath()
	fmt.Printf("scope:     %s (init=launchd)\n", c.Scope)
	fmt.Printf("plist:     %s%s\n", plistPath, existsMark(plistPath))
	fmt.Printf("env file:  %s%s\n", c.EnvFile, existsMark(c.EnvFile))
	fmt.Println()

	out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	if err != nil {
		fmt.Printf("launchctl list %s: not loaded\n", launchdLabel)
		return nil
	}
	fmt.Printf("launchctl list %s:\n%s", launchdLabel, string(out))
	return nil
}

func launchctlLoad(plistPath string, uid int) error {
	if err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), plistPath).Run(); err == nil {
		return nil
	}
	return exec.Command("launchctl", "load", "-w", plistPath).Run()
}

func launchctlUnload(plistPath string, uid int) error {
	if err := exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", uid), plistPath).Run(); err == nil {
		return nil
	}
	return exec.Command("launchctl", "unload", "-w", plistPath).Run()
}

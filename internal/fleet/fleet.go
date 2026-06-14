// fleet.go — `agenthubctl fleet` command tree + init-system dispatch.
package fleet

import (
	"fmt"
	"os"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/spf13/cobra"
)

// NewFleetCmd builds the `fleet` parent command with install/uninstall/status.
func NewFleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Manage the bridge fleet boot-start + watchdog (systemd/launchd)",
		Long: "fleet installs a portable boot-start + watchdog that periodically runs the\n" +
			"idempotent `bridge start --all`, reviving the fleet after reboots / sleep.\n" +
			"User, HOME, and the agenthubctl path are resolved at install time — no\n" +
			"machine-specific paths are baked in. Secrets live in an EnvironmentFile\n" +
			"(~/.agent-hub/fleet.env), never in the generated unit.",
	}
	cmd.AddCommand(newInstallCmd(), newUninstallCmd(), newStatusCmd())
	return cmd
}

func newInstallCmd() *cobra.Command {
	var (
		opt    ResolveOptions
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install boot-start + watchdog for `bridge start --all`",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := Resolve(opt)
			if err != nil {
				return err
			}
			if !dryRun {
				warnIfNoBridges()
			}
			return c.Install(dryRun)
		},
	}
	cmd.Flags().BoolVar(&opt.ForceSystem, "system", false, "force system-level install (/etc/systemd/system; needs root)")
	cmd.Flags().BoolVar(&opt.ForceUser, "user", false, "force user-level install (~/.config/systemd/user)")
	cmd.Flags().BoolVar(&opt.Force, "force", false, "if a different-scope install already exists, uninstall it first instead of aborting")
	cmd.Flags().StringVar(&opt.Binary, "binary", "", "agenthubctl path baked into the unit (default: this executable)")
	cmd.Flags().StringVar(&opt.EnvFile, "env-file", "", "EnvironmentFile path (default: ~/.agent-hub/fleet.env)")
	cmd.Flags().IntVar(&opt.Timeout, "timeout", 0, "per-bridge start timeout in seconds (default 40)")
	cmd.Flags().IntVar(&opt.Interval, "watchdog-interval", 0, "watchdog interval in seconds (default 180)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the generated files + actions, write nothing")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	var opt ResolveOptions
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the fleet boot-start + watchdog (env file is preserved)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := Resolve(opt)
			if err != nil {
				return err
			}
			return c.Uninstall()
		},
	}
	cmd.Flags().BoolVar(&opt.ForceSystem, "system", false, "target system-level install")
	cmd.Flags().BoolVar(&opt.ForceUser, "user", false, "target user-level install")
	cmd.Flags().StringVar(&opt.EnvFile, "env-file", "", "EnvironmentFile path (default: ~/.agent-hub/fleet.env)")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var opt ResolveOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether the fleet boot-start + watchdog is installed and active",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := Resolve(opt)
			if err != nil {
				return err
			}
			return c.Status()
		},
	}
	cmd.Flags().BoolVar(&opt.ForceSystem, "system", false, "target system-level install")
	cmd.Flags().BoolVar(&opt.ForceUser, "user", false, "target user-level install")
	cmd.Flags().StringVar(&opt.EnvFile, "env-file", "", "EnvironmentFile path (default: ~/.agent-hub/fleet.env)")
	return cmd
}

// Install dispatches to the resolved init system.
func (c *Config) Install(dryRun bool) error {
	switch c.Init {
	case initSystemd:
		return c.installSystemd(dryRun)
	case initLaunchd:
		return c.installLaunchd(dryRun)
	}
	return fmt.Errorf("unsupported init system %q", c.Init)
}

func (c *Config) Uninstall() error {
	switch c.Init {
	case initSystemd:
		return c.uninstallSystemd()
	case initLaunchd:
		return c.uninstallLaunchd()
	}
	return fmt.Errorf("unsupported init system %q", c.Init)
}

func (c *Config) Status() error {
	switch c.Init {
	case initSystemd:
		return c.statusSystemd()
	case initLaunchd:
		return c.statusLaunchd()
	}
	return fmt.Errorf("unsupported init system %q", c.Init)
}

// genFile is a (path, content) pair printed by dry-run.
type genFile struct {
	path    string
	content string
}

func printDryRun(c *Config, files []genFile, actions []string) {
	fmt.Printf("DRY RUN — %s/%s, scope=%s, user=%s\n", c.OS, c.Init, c.Scope, c.User)
	fmt.Printf("binary: %s\n\n", c.Binary)
	for _, f := range files {
		fmt.Printf("# ==== %s ====\n%s\n", f.path, f.content)
	}
	fmt.Println("# ==== actions ====")
	for _, a := range actions {
		fmt.Printf("  %s\n", a)
	}
}

// warnIfNoBridges nudges the operator to register a desired-state fleet first; the
// watchdog has nothing to start otherwise. Non-fatal.
func warnIfNoBridges() {
	cfgs, err := bridgecfg.ListAll()
	if err == nil && len(cfgs) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no bridges registered — the watchdog will have nothing to start.")
		fmt.Fprintln(os.Stderr, "  register desired-state first: agenthubctl bridge config set <handle> -w <workdir>")
		fmt.Fprintln(os.Stderr)
	}
}

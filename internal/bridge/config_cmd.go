// config_cmd.go — bridge config set/get/list/delete subcommands
package bridge

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/spf13/cobra"
)

func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage per-bridge saved configuration",
	}
	cmd.AddCommand(
		newConfigSetCmd(),
		newConfigGetCmd(),
		newConfigListCmd(),
		newConfigDeleteCmd(),
	)
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	var (
		workdir     string
		tenant      string
		bridgeType  string
		displayName string
	)

	cmd := &cobra.Command{
		Use:   "set <handle>",
		Short: "Save defaults for a bridge handle",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			handle := args[0]
			if !validHandle.MatchString(handle) {
				return fmt.Errorf("invalid handle %q: only [a-zA-Z0-9_-] allowed", handle)
			}

			// Load existing config so we only overwrite fields that are explicitly set.
			existing, err := bridgecfg.Load(handle)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if existing == nil {
				existing = &bridgecfg.BridgeConfig{Handle: handle}
			}

			if workdir != "" {
				existing.Workdir = workdir
			}
			if tenant != "" {
				existing.Tenant = tenant
			}
			if bridgeType != "" {
				existing.BridgeType = bridgeType
			}
			if displayName != "" {
				existing.DisplayName = displayName
			}

			if err := bridgecfg.Save(existing); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Printf("saved config for @%s\n", handle)
			return nil
		},
	}

	cmd.Flags().StringVarP(&workdir, "workdir", "w", "", "default workdir for this bridge")
	cmd.Flags().StringVar(&tenant, "tenant", "", "default tenant ID")
	cmd.Flags().StringVar(&bridgeType, "type", "", "bridge type (default: bridge-claude2)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name passed on spawn")
	return cmd
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <handle>",
		Short: "Show saved config for a bridge handle",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := bridgecfg.Load(args[0])
			if err != nil {
				return err
			}
			if cfg == nil {
				return fmt.Errorf("no config saved for @%s", args[0])
			}
			bridgeType := cfg.BridgeType
			if bridgeType == "" {
				bridgeType = defaultBridgeType
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "handle\t%s\n", cfg.Handle)
			fmt.Fprintf(w, "workdir\t%s\n", cfg.Workdir)
			fmt.Fprintf(w, "tenant\t%s\n", cfg.Tenant)
			fmt.Fprintf(w, "type\t%s\n", bridgeType)
			fmt.Fprintf(w, "display_name\t%s\n", cfg.DisplayName)
			return w.Flush()
		},
	}
}

func newConfigListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all saved bridge configs",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfgs, err := bridgecfg.ListAll()
			if err != nil {
				return err
			}
			if len(cfgs) == 0 {
				fmt.Println("no bridge configs saved")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "HANDLE\tTYPE\tTENANT\tWORKDIR\tDISPLAY_NAME")
			for _, cfg := range cfgs {
				bridgeType := cfg.BridgeType
				if bridgeType == "" {
					bridgeType = defaultBridgeType
				}
				fmt.Fprintf(w, "@%s\t%s\t%s\t%s\t%s\n",
					cfg.Handle, bridgeType, cfg.Tenant, cfg.Workdir, cfg.DisplayName)
			}
			return w.Flush()
		},
	}
}

func newConfigDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <handle>",
		Short: "Remove saved config for a bridge handle",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := bridgecfg.Delete(args[0]); err != nil {
				return err
			}
			fmt.Printf("deleted config for @%s\n", args[0])
			return nil
		},
	}
}

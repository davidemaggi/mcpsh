package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/davidemaggi/mcpsh/internal/daemon"
	"github.com/davidemaggi/mcpsh/internal/update"
	"github.com/davidemaggi/mcpsh/internal/version"
)

const updateTimeout = 5 * time.Minute

// updateInfo is the JSON form of "mcpsh self-update".
type updateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"updateAvailable"`
	Updated   bool   `json:"updated"`
	Method    string `json:"installedWith,omitempty"`
	Command   string `json:"updateCommand,omitempty"`
	Asset     string `json:"asset,omitempty"`
}

func (a *App) selfUpdateCmd() *cobra.Command {
	var check, force bool
	var want string
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update mcpsh to the latest release",
		Long: `Download the latest release (or --version), verify its checksum and replace this mcpsh.

On Windows the Authenticode-signed build is preferred. When a package manager (Homebrew, Scoop,
npm, winget) installed mcpsh, self-update says which command updates it instead.
The background daemon is replaced automatically on the next call.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), updateTimeout)
			defer cancel()
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			info := updateInfo{Current: version.Version}
			info.Method, info.Command = update.Method(exe, version.Version, os.Getenv)

			u := update.New(os.Getenv)
			target := want
			if target == "" {
				if target, err = u.Latest(ctx); err != nil {
					return err
				}
			}
			info.Latest = target
			info.Available = update.Newer(target, version.Version)
			if check || (!info.Available && !force && want == "") {
				return a.reportUpdate(info, check)
			}
			if info.Method != "" && !force {
				return &update.ManagedError{Method: info.Method, Command: info.Command}
			}
			data, asset, err := u.Download(ctx, target)
			if err != nil {
				return err
			}
			if err := update.Replace(exe, data); err != nil {
				return fmt.Errorf("%w (try again with permission to write %s)", err, filepath.Dir(exe))
			}
			info.Updated, info.Asset = true, asset
			// The old daemon would be replaced on the next call anyway; stopping it now frees
			// the old executable at once.
			sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
			_, _ = daemon.NewClient(0).Stop(sctx)
			scancel()
			update.CleanupOld(exe)
			return a.reportUpdate(info, false)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether an update is available")
	cmd.Flags().StringVar(&want, "version", "", "install this version instead of the latest (also to downgrade)")
	cmd.Flags().BoolVar(&force, "force", false, "replace the executable even if a package manager installed it, or reinstall the same version")
	return cmd
}

func (a *App) reportUpdate(info updateInfo, check bool) error {
	if a.jsonOutput() {
		return writeJSON(a.Stdout, info)
	}
	w := a.Stdout
	switch {
	case info.Updated:
		fmt.Fprintf(w, "Updated mcpsh %s -> %s (%s).\n", info.Current, info.Latest, info.Asset)
	case info.Available:
		fmt.Fprintf(w, "mcpsh %s is available (this is %s).\n", info.Latest, info.Current)
		if info.Command != "" {
			fmt.Fprintf(w, "Installed with %s: update with %q.\n", info.Method, info.Command)
		} else if check {
			fmt.Fprintln(w, "Run \"mcpsh self-update\" to install it.")
		}
	default:
		fmt.Fprintf(w, "mcpsh %s is up to date.\n", info.Current)
	}
	return nil
}

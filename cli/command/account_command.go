package command

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"codebuddy-gateway/core"
	"codebuddy-gateway/model"
	"codebuddy-gateway/service"

	"github.com/spf13/cobra"
)

func NewAccountCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Import and list local CodeBuddy accounts",
	}
	cmd.AddCommand(newAccountImportCommand(), newAccountImportDesktopCommand(), newAccountListCommand())
	return cmd
}

func newAccountImportCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:          "import <file-or-dir>",
		Short:        "Import accounts from CodeBuddy / WorkBuddy JSON",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := service.LoadImportedAccounts(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("parsed %d account(s) from %s\n", len(items), args[0])
			for _, item := range items {
				fmt.Printf("- %s jwt=%s refresh=%s\n", item.Name, service.MaskToken(item.JWT), service.MaskToken(item.RefreshToken))
			}
			if dryRun {
				fmt.Println("dry-run: skip database")
				return nil
			}
			Bootstrap(cmd)
			created, updated := 0, 0
			for _, item := range items {
				acc, isNew, err := service.UpsertAccount(item.ToModel())
				if err != nil {
					return err
				}
				if acc == nil {
					continue
				}
				if isNew {
					created++
				} else {
					updated++
				}
			}
			fmt.Printf("imported created=%d updated=%d\n", created, updated)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "parse only, do not write the database")
	return cmd
}

func newAccountImportDesktopCommand() *cobra.Command {
	var dryRun bool
	var path string
	cmd := &cobra.Command{
		Use:          "import-desktop",
		Short:        "Import existing CodeBuddy / WorkBuddy desktop login state",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := service.LoadDesktopAuthAccounts(path)
			if err != nil {
				return err
			}
			fmt.Printf("found %d desktop account(s)\n", len(items))
			for _, item := range items {
				fmt.Printf("- %s jwt=%s refresh=%s\n", item.Name, service.MaskToken(item.JWT), service.MaskToken(item.RefreshToken))
			}
			if dryRun {
				fmt.Println("dry-run: skip database")
				return nil
			}
			Bootstrap(cmd)
			created, updated, err := service.ImportDesktopAccounts(items)
			if err != nil {
				return err
			}
			fmt.Printf("imported created=%d updated=%d\n", created, updated)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "desktop auth directory override")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "parse only, do not write the database")
	return cmd
}

// TryImportDesktopAccounts is used by the Windows double-click flow. A missing
// desktop auth snapshot is not an error: the caller can fall back to browser
// login. Malformed snapshots are also returned so the caller can report them.
func TryImportDesktopAccounts(configPath string) (created, updated int, found bool, err error) {
	items, err := service.LoadDesktopAuthAccounts("")
	if err != nil {
		if errors.Is(err, service.ErrDesktopAuthNotFound) {
			return 0, 0, false, nil
		}
		return 0, 0, false, err
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("config", configPath, "")
	cmd.Flags().String("api-key", "", "")
	cmd.Flags().String("admin-key", "", "")
	cmd.Flags().Bool("dev", false, "")
	Bootstrap(cmd)
	defer core.CloseDB()
	created, updated, err = service.ImportDesktopAccounts(items)
	return created, updated, true, err
}

func newAccountListCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List local accounts",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			Bootstrap(cmd)
			list, err := model.ListAccounts()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tUSERNAME\tSTATUS\tJWT\tEXPIRES")
			for _, acc := range list {
				exp := ""
				if acc.JWTExpiresAt != nil {
					exp = acc.JWTExpiresAt.Format("2006-01-02")
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", acc.ID, acc.Name, acc.Username, acc.Status, service.MaskToken(acc.JWT), exp)
			}
			return w.Flush()
		},
	}
}

package command

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"codebuddy-gateway/model"
	"codebuddy-gateway/service"

	"github.com/spf13/cobra"
)

func newAccountTaskCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage CodeBuddy growth tasks",
	}
	cmd.AddCommand(
		newAccountTaskListCommand(),
		newAccountTaskRunCommand(),
		newAccountTaskClaimCommand(),
		newAccountTaskAcceptCommand(),
		newAccountTaskBatchCommand(),
		newAccountTaskStatusCommand(),
	)
	return cmd
}

func newAccountTaskListCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "list <account-id>",
		Short:        "List growth tasks for an account",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			Bootstrap(cmd)
			service.InitRuntime()
			acc, err := loadTaskAccount(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			tasks, err := service.DefaultClient.GrowthListTasks(ctx, acc)
			if err != nil {
				return err
			}
			service.SortTasksByReward(tasks)
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CODE\tDESC\tSTATUS\tPROGRESS\tREWARD\tCLAIMED")
			for _, task := range tasks {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t+%d/+%d\t%t\n",
					task.TaskCode, task.TaskDesc, task.AcceptStatus, task.Current, task.Target, task.Credit, task.Energy, task.Claimed)
			}
			return w.Flush()
		},
	}
}

func newAccountTaskRunCommand() *cobra.Command {
	var codes string
	cmd := &cobra.Command{
		Use:          "run <account-id>",
		Short:        "Run automated growth tasks for an account",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			Bootstrap(cmd)
			service.InitRuntime()
			acc, err := loadTaskAccount(args[0])
			if err != nil {
				return err
			}
			summary := service.DefaultClient.RunAccountTasks(cmd.Context(), acc, splitCodes(codes))
			if summary.Err != "" {
				return fmt.Errorf("%s", summary.Err)
			}
			for _, result := range summary.Results {
				status := "OK"
				if result.Skipped {
					status = "SKIP"
				}
				if result.Error != "" {
					status = "ERROR"
				}
				fmt.Printf("[%s] %s (%s): %s%s\n", status, result.Code, result.Desc, result.Message, suffixMessage(result.Error))
			}
			fmt.Printf("completed claimed=%d credit=%d energy=%d\n", len(summary.Claimed), summary.CreditGained, summary.EnergyGained)
			return nil
		},
	}
	cmd.Flags().StringVar(&codes, "code", "", "comma-separated task codes; empty runs all automated tasks")
	return cmd
}

func newAccountTaskClaimCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "claim <account-id> <task-code>",
		Short:        "Claim one completed growth task",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			Bootstrap(cmd)
			service.InitRuntime()
			acc, err := loadTaskAccount(args[0])
			if err != nil {
				return err
			}
			credit, energy, err := service.DefaultClient.GrowthClaimReward(cmd.Context(), acc, args[1])
			if err != nil {
				return err
			}
			fmt.Printf("claimed %s credit=%d energy=%d\n", args[1], credit, energy)
			return nil
		},
	}
}

func newAccountTaskAcceptCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "accept <account-id>",
		Short:        "Accept all pending growth tasks",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			Bootstrap(cmd)
			service.InitRuntime()
			acc, err := loadTaskAccount(args[0])
			if err != nil {
				return err
			}
			tasks, err := service.DefaultClient.GrowthListTasks(cmd.Context(), acc)
			if err != nil {
				return err
			}
			var codes []string
			for _, task := range tasks {
				if task.Claimed || task.Locked || task.AcceptStatus == "accepted" || task.AcceptStatus == "completed" {
					continue
				}
				codes = append(codes, task.TaskCode)
			}
			if len(codes) == 0 {
				fmt.Println("accepted=0")
				return nil
			}
			if err := service.DefaultClient.GrowthAcceptTasks(cmd.Context(), acc, codes); err != nil {
				return err
			}
			fmt.Printf("accepted=%d codes=%s\n", len(codes), strings.Join(codes, ","))
			return nil
		},
	}
}

func newAccountTaskBatchCommand() *cobra.Command {
	var codes string
	var concurrency int
	cmd := &cobra.Command{
		Use:          "batch",
		Short:        "Run growth tasks for all enabled accounts",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			Bootstrap(cmd)
			service.InitRuntime()
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			quit := make(chan os.Signal, 1)
			signal.Notify(quit, os.Interrupt)
			defer signal.Stop(quit)
			go func() {
				select {
				case <-quit:
					cancel()
				case <-ctx.Done():
				}
			}()
			state, err := service.RunBatchTasks(ctx, splitCodes(codes), concurrency)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ACCOUNT\tSTATUS\tCREDIT\tENERGY\tMESSAGE")
			for _, result := range state.Results {
				fmt.Fprintf(w, "%s (#%d)\t%s\t%d\t%d\t%s\n", result.Account, result.AccountID, result.Status, result.Credit, result.Energy, result.Message)
			}
			fmt.Printf("total=%d done=%d credit=%d energy=%d\n", state.Total, state.Done, state.CreditGained, state.EnergyGained)
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&codes, "code", "", "comma-separated task codes; empty runs all automated tasks")
	cmd.Flags().IntVar(&concurrency, "concurrency", 3, "number of accounts to process concurrently (max 8)")
	return cmd
}

func newAccountTaskStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show the latest batch task state",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			Bootstrap(cmd)
			service.InitRuntime()
			state := service.BatchTasksStatus()
			if state == nil {
				fmt.Println("no batch task has run in this process")
				return nil
			}
			fmt.Printf("running=%t total=%d done=%d credit=%d energy=%d started=%s\n",
				state.Running, state.Total, state.Done, state.CreditGained, state.EnergyGained, state.StartedAt.Format(time.RFC3339))
			return nil
		},
	}
}

func loadTaskAccount(raw string) (*model.Account, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || id == 0 {
		return nil, fmt.Errorf("invalid account ID: %s", raw)
	}
	acc, err := model.GetAccountByID(uint(id))
	if err != nil || acc == nil {
		return nil, fmt.Errorf("account not found: %s", raw)
	}
	return acc, nil
}

func splitCodes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if code := strings.TrimSpace(part); code != "" {
			out = append(out, code)
		}
	}
	return out
}

func suffixMessage(message string) string {
	if message == "" {
		return ""
	}
	return " (" + message + ")"
}

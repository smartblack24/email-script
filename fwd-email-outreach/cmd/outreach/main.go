package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/campaign"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/config"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/contacts"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/gmail"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/importer"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/scheduler"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/sender"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/suppression"
	tpl "github.com/smartblack24/email-script/fwd-email-outreach/internal/templates"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/tracker"
)

var (
	cfgPath      string
	campaignName string
	useMockGmail bool
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	root := &cobra.Command{
		Use:   "outreach",
		Short: "FWD Consulting Group Gmail outreach CLI",
		Long:  "CLI-only Gmail outreach automation for FWD Consulting Group. Default mode is dry-run; use --execute for live sends.",
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&cfgPath, "env", ".env", "path to .env file")
	root.PersistentFlags().StringVar(&campaignName, "campaign", "", "campaign name (defaults to DEFAULT_CAMPAIGN_NAME)")
	root.PersistentFlags().BoolVar(&useMockGmail, "mock-gmail", false, "use in-memory Gmail mock (for tests/local demos)")

	root.AddCommand(
		cmdAuth(logger),
		cmdImport(logger),
		cmdContacts(logger),
		cmdPreview(logger),
		cmdSend(logger),
		cmdWorker(logger),
		cmdStatus(logger),
		cmdExport(logger),
		cmdSuppress(logger),
		cmdCampaign(logger),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func loadApp(logger *slog.Logger) (*config.Config, *gorm.DB, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if campaignName == "" {
		campaignName = cfg.DefaultCampaignName
	}
	db, err := database.Open(cfg.DatabasePath)
	if err != nil {
		return nil, nil, err
	}
	_ = logger
	return cfg, db, nil
}

func loadTemplates(cfg *config.Config) (*tpl.Renderer, error) {
	dir := cfg.TemplatesDir
	if !filepath.IsAbs(dir) {
		if _, err := os.Stat(dir); err != nil {
			// try relative to executable working directory already; fall through
		}
	}
	return tpl.Load(dir)
}

func maybeMail(ctx context.Context, cfg *config.Config, db *gorm.DB, logger *slog.Logger) (gmail.MailClient, error) {
	if useMockGmail {
		email := cfg.SenderEmail
		if email == "" {
			email = "mock-sender@fwd.example"
		}
		return gmail.NewMockClient(email), nil
	}
	if _, err := os.Stat(cfg.GmailTokenFile); err != nil {
		return nil, nil
	}
	return gmail.Authenticate(ctx, gmail.AuthConfig{
		CredentialsFile: cfg.GmailCredentialsFile,
		TokenFile:       cfg.GmailTokenFile,
	}, db, logger)
}

func cmdAuth(logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with Gmail via OAuth 2.0",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := gmail.Authenticate(ctx, gmail.AuthConfig{
				CredentialsFile: cfg.GmailCredentialsFile,
				TokenFile:       cfg.GmailTokenFile,
			}, db, logger)
			if err != nil {
				return err
			}
			fmt.Printf("Authenticated as %s\n", client.SenderEmail())
			fmt.Println("Token saved. You can now run send --execute (after setting DRY_RUN=false and compliance fields).")
			return nil
		},
	}
}

func cmdImport(logger *slog.Logger) *cobra.Command {
	var file string
	var permission string
	c := &cobra.Command{
		Use:   "import",
		Short: "Import contacts from Excel or CSV",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			sum, err := importer.ImportFile(file, importer.Options{ColumnMapOverrides: cfg.ColumnMapOverrides})
			if err != nil {
				return err
			}
			inserted, updated, err := contacts.PersistImport(db, sum, permission)
			if err != nil {
				return err
			}
			fmt.Printf("Import summary for %s\n", sum.SourceFile)
			fmt.Printf("  rows=%d valid=%d invalid=%d duplicates=%d\n", sum.TotalRows, sum.Valid, sum.Invalid, sum.Duplicates)
			fmt.Printf("  inserted=%d updated=%d\n", inserted, updated)
			if len(sum.AmbiguousHeaders) > 0 {
				fmt.Println("  ambiguous headers:")
				for _, a := range sum.AmbiguousHeaders {
					fmt.Printf("    - %s\n", a)
				}
			}
			for reason, n := range sum.SkippedReasons {
				fmt.Printf("  skip[%s]=%d\n", reason, n)
			}
			return nil
		},
	}
	c.Flags().StringVar(&file, "file", "", "path to contacts.xlsx or contacts.csv")
	c.Flags().StringVar(&permission, "permission-note", "imported for FWD Consulting Group outreach; approval required before send", "source/permission note")
	_ = c.MarkFlagRequired("file")
	return c
}

func cmdContacts(logger *slog.Logger) *cobra.Command {
	parent := &cobra.Command{Use: "contacts", Short: "Manage contacts"}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List contacts",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			approvedOnly, _ := cmd.Flags().GetBool("approved")
			limit, _ := cmd.Flags().GetInt("limit")
			var filter contacts.ListFilter
			filter.Limit = limit
			if approvedOnly {
				t := true
				filter.Approved = &t
			}
			items, err := contacts.List(db, filter)
			if err != nil {
				return err
			}
			fmt.Printf("%-6s %-28s %-36s %-8s %s\n", "ID", "NAME", "EMAIL", "APPROVED", "COUNTRY")
			for _, c := range items {
				fmt.Printf("%-6d %-28s %-36s %-8v %s\n", c.ID, truncate(c.FullName, 28), truncate(c.Email, 36), c.Approved, c.Country)
			}
			fmt.Printf("Total: %d\n", len(items))
			return nil
		},
	}
	listCmd.Flags().Bool("approved", false, "only approved contacts")
	listCmd.Flags().Int("limit", 100, "max rows")

	approveCmd := &cobra.Command{
		Use:   "approve",
		Short: "Approve contacts listed in a CSV (Email column required)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			file, _ := cmd.Flags().GetString("file")
			approved, missing, err := contacts.ApproveFromCSV(db, file)
			if err != nil {
				return err
			}
			fmt.Printf("Approved=%d missing_or_suppressed=%d\n", approved, missing)
			return nil
		},
	}
	approveCmd.Flags().String("file", "", "approved.csv path")
	_ = approveCmd.MarkFlagRequired("file")

	parent.AddCommand(listCmd, approveCmd)
	return parent
}

func cmdPreview(logger *slog.Logger) *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "preview",
		Short: "Preview rendered outreach emails without sending",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			renderer, err := loadTemplates(cfg)
			if err != nil {
				return err
			}
			svc := &sender.Service{DB: db, Cfg: cfg, Templates: renderer, Logger: logger}
			items, err := svc.Preview(cmd.Context(), campaignName, limit)
			if err != nil {
				return err
			}
			for i, it := range items {
				fmt.Printf("--- preview %d/%d ---\n", i+1, len(items))
				fmt.Printf("To: %s (%s)\n", it.Email, it.FullName)
				fmt.Printf("Subject: %s\n\n%s\n\n", it.Subject, it.Body)
			}
			fmt.Printf("Previews: %d\n", len(items))
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 10, "max previews")
	return c
}

func cmdSend(logger *slog.Logger) *cobra.Command {
	var limit int
	var dryRun bool
	var execute bool
	c := &cobra.Command{
		Use:   "send",
		Short: "Send outreach emails (dry-run by default; require --execute for live)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun && execute {
				return fmt.Errorf("specify only one of --dry-run or --execute")
			}
			if !dryRun && !execute {
				dryRun = true // default safety
			}
			cfg, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			renderer, err := loadTemplates(cfg)
			if err != nil {
				return err
			}
			mail, err := maybeMail(cmd.Context(), cfg, db, logger)
			if err != nil {
				return err
			}
			if execute && mail == nil {
				return fmt.Errorf("gmail not authenticated; run `outreach auth` first (or pass --mock-gmail for demos)")
			}
			svc := &sender.Service{DB: db, Cfg: cfg, Mail: mail, Templates: renderer, Logger: logger}
			res, err := svc.SendInitial(cmd.Context(), sender.SendOptions{
				CampaignName: campaignName,
				Limit:        limit,
				Execute:      execute,
			})
			if err != nil {
				return err
			}
			mode := "dry-run"
			if execute {
				mode = "execute"
			}
			fmt.Printf("Send complete (%s): sent=%d skipped=%d failed=%d deferred=%d\n", mode, res.Sent, res.Skipped, res.Failed, res.Deferred)
			for _, d := range res.Details {
				fmt.Printf("  - %s\n", d)
			}
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 20, "max emails this run")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "render and record dry-run attempts without calling Gmail")
	c.Flags().BoolVar(&execute, "execute", false, "actually send via Gmail (requires auth + compliance config + DRY_RUN=false)")
	return c
}

func cmdWorker(logger *slog.Logger) *cobra.Command {
	var once bool
	var interval time.Duration
	var execute bool
	c := &cobra.Command{
		Use:   "worker",
		Short: "Background worker for follow-ups, reply checks, and stale claim recovery",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			renderer, err := loadTemplates(cfg)
			if err != nil {
				return err
			}
			mail, err := maybeMail(cmd.Context(), cfg, db, logger)
			if err != nil {
				return err
			}
			svc := &sender.Service{DB: db, Cfg: cfg, Mail: mail, Templates: renderer, Logger: logger}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			runCycle := func() error {
				if n, err := scheduler.RecoverStaleClaims(db, 15*time.Minute); err != nil {
					return err
				} else if n > 0 {
					logger.Info("recovered stale follow-up claims", "count", n)
				}
				if mail != nil {
					if n, err := tracker.CheckReplies(db, mail); err != nil {
						logger.Warn("reply check error", "err", err)
					} else if n > 0 {
						logger.Info("replies detected", "count", n)
					}
				}
				jobs, err := scheduler.ClaimDueJobs(db, 5)
				if err != nil {
					return err
				}
				for i := range jobs {
					job := jobs[i]
					dry := !execute
					if err := svc.SendFollowupJob(ctx, &job, dry); err != nil {
						logger.Warn("follow-up job error", "job", job.ID, "err", err)
					} else {
						logger.Info("follow-up processed", "job", job.ID, "step", job.Step, "dry_run", dry)
					}
				}
				return nil
			}

			if once {
				return runCycle()
			}
			fmt.Println("Worker running. Ctrl+C to stop.")
			t := time.NewTicker(interval)
			defer t.Stop()
			if err := runCycle(); err != nil {
				return err
			}
			for {
				select {
				case <-ctx.Done():
					fmt.Println("Worker stopped.")
					return nil
				case <-t.C:
					if err := runCycle(); err != nil {
						logger.Error("worker cycle failed", "err", err)
					}
				}
			}
		},
	}
	c.Flags().BoolVar(&once, "once", false, "run a single cycle and exit")
	c.Flags().DurationVar(&interval, "interval", 1*time.Minute, "poll interval")
	c.Flags().BoolVar(&execute, "execute", false, "send live follow-ups (default dry-run)")
	return c
}

func cmdStatus(logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show outreach status summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			rep, err := tracker.Collect(db)
			if err != nil {
				return err
			}
			fmt.Printf("Contacts: total=%d approved=%d suppressed=%d\n", rep.ContactsTotal, rep.ContactsApproved, rep.Suppressed)
			fmt.Printf("Messages: sent=%d dry_run=%d\n", rep.MessagesSent, rep.MessagesDryRun)
			fmt.Printf("Follow-ups: pending=%d claimed=%d done=%d\n", rep.FollowupsPending, rep.FollowupsClaimed, rep.FollowupsDone)
			if rep.LastSendAt != "" {
				fmt.Printf("Last send at: %s\n", rep.LastSendAt)
			}
			for _, c := range rep.Campaigns {
				fmt.Printf("Campaign %s (%s) status=%s pending=%d sent=%d replied=%d failed=%d stopped=%d\n",
					c.Name, c.ID, c.Status, c.Pending, c.Sent, c.Replied, c.Failed, c.Stopped)
			}
			return nil
		},
	}
}

func cmdExport(logger *slog.Logger) *cobra.Command {
	var file string
	c := &cobra.Command{
		Use:   "export",
		Short: "Export contacts, messages, suppression, and follow-ups to Excel",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			if err := tracker.ExportExcel(db, file); err != nil {
				return err
			}
			fmt.Printf("Wrote report to %s\n", file)
			return nil
		},
	}
	c.Flags().StringVar(&file, "file", "report.xlsx", "output .xlsx path")
	return c
}

func cmdSuppress(logger *slog.Logger) *cobra.Command {
	var email string
	var reason string
	c := &cobra.Command{
		Use:   "suppress",
		Short: "Add an email to the permanent suppression list",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			if err := suppression.Add(db, email, reason, "cli"); err != nil {
				return err
			}
			fmt.Printf("Suppressed %s (%s)\n", email, reason)
			return nil
		},
	}
	c.Flags().StringVar(&email, "email", "", "email to suppress")
	c.Flags().StringVar(&reason, "reason", "manual_opt_out", "suppression reason")
	_ = c.MarkFlagRequired("email")
	return c
}

func cmdCampaign(logger *slog.Logger) *cobra.Command {
	parent := &cobra.Command{Use: "campaign", Short: "Manage named campaigns"}

	pauseCmd := &cobra.Command{
		Use:   "pause [name-or-id]",
		Short: "Pause a campaign",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			name := campaignName
			if len(args) > 0 {
				name = args[0]
			}
			c, err := campaign.Pause(db, name)
			if err != nil {
				return err
			}
			fmt.Printf("Paused campaign %s (%s)\n", c.Name, c.ID)
			return nil
		},
	}
	resumeCmd := &cobra.Command{
		Use:   "resume [name-or-id]",
		Short: "Resume a campaign",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := loadApp(logger)
			if err != nil {
				return err
			}
			name := campaignName
			if len(args) > 0 {
				name = args[0]
			}
			c, err := campaign.Resume(db, name)
			if err != nil {
				return err
			}
			fmt.Printf("Resumed campaign %s (%s)\n", c.Name, c.ID)
			return nil
		},
	}
	parent.AddCommand(pauseCmd, resumeCmd)
	return parent
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

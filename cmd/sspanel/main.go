// Command sspanel runs a self-hosted Shadowsocks server with an embedded web
// panel.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/config"
	"github.com/ydong/ssrservice/internal/logging"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/panel"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
	"github.com/ydong/ssrservice/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `sspanel %s - Shadowsocks server with a web panel

Usage:
  sspanel run [--config path]
  sspanel check [--config path]
  sspanel repair [--config path]
  sspanel admin reset-password [--config path]
  sspanel version

`, version.Full())
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("a command is required")
	}
	switch args[0] {
	case "run":
		return runServer(args[1:])
	case "check":
		return runCheck(args[1:])
	case "repair":
		return runRepair(args[1:])
	case "admin":
		return runAdmin(args[1:])
	case "version":
		fmt.Println(version.Info())
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func newFlagSet(name string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	dbPath := fs.String("db", "", "override database path")
	return fs, configPath, dbPath
}

func loadConfig(fs *flag.FlagSet, args []string, configPath, dbPath *string) (*config.Config, error) {
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return nil, err
	}
	if *dbPath != "" {
		cfg.Database.Path = *dbPath
	}
	if err := cfg.EnsureDataDir(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func runServer(args []string) error {
	fs, configPath, dbPath := newFlagSet("run")
	cfg, err := loadConfig(fs, args, configPath, dbPath)
	if err != nil {
		return err
	}
	logger, _, err := logging.New(cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		return err
	}
	logger.Info("starting sspanel", "version", version.Full(), "config", *configPath)

	st, err := store.Open(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer st.Close()

	mgr := ssserver.New(ssserver.Options{
		Logger:          logger,
		Sink:            st,
		Location:        cfg.Location(),
		FlushInterval:   cfg.Traffic.FlushInterval.Duration(),
		EnforceInterval: cfg.Traffic.EnforceInterval.Duration(),
		UDPTimeout:      cfg.Traffic.UDPTimeout.Duration(),
		Policy: ssserver.PolicyConfig{
			AllowPrivateTargets: cfg.Security.AllowPrivateTargets,
			BlockMetadata:       cfg.Security.BlockMetadata,
			PanelListen:         cfg.Panel.Listen,
			PanelPort:           cfg.Panel.Port,
		},
	})
	svc := service.New(st, mgr, cfg, logger)
	defer svc.Stop()

	generatedPassword, err := svc.Bootstrap()
	if err != nil {
		return err
	}
	if generatedPassword != "" {
		fmt.Printf("\n============================================================\n")
		fmt.Printf(" 管理员已创建 / administrator created\n")
		fmt.Printf(" 用户名 / username: admin\n")
		fmt.Printf(" 密码   / password: %s\n", generatedPassword)
		fmt.Printf(" 首次登录后必须修改密码 / change it after first login\n")
		fmt.Printf(" 面板地址 / panel: http://%s\n", panelAddress(cfg))
		fmt.Printf("============================================================\n\n")
	}

	secret, err := svc.SessionSecret()
	if err != nil {
		return err
	}
	sessions, err := auth.NewSessions(secret, svc.SessionTTL())
	if err != nil {
		return err
	}

	panelSrv := panel.New(cfg, svc, logger, sessions)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("panel listening", "address", panelSrv.Addr(), "base_path", cfg.Panel.BasePath)
		errCh <- panelSrv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("panel server: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := panelSrv.Shutdown(ctx); err != nil {
		logger.Warn("panel shutdown failed", "error", err)
	}
	svc.Stop()
	logger.Info("stopped")
	return nil
}

func panelAddress(cfg *config.Config) string {
	host := cfg.Panel.Listen
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d%s", host, cfg.Panel.Port, cfg.Panel.BasePath)
}

func runCheck(args []string) error {
	fs, configPath, dbPath := newFlagSet("check")
	cfg, err := loadConfig(fs, args, configPath, dbPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()
	inbounds, err := st.ListInbounds()
	if err != nil {
		return err
	}
	clients, err := st.ListClients(0)
	if err != nil {
		return err
	}
	admins, err := st.AdminCount()
	if err != nil {
		return err
	}
	fmt.Printf("config ok\n")
	fmt.Printf("database: %s\n", cfg.Database.Path)
	fmt.Printf("panel: http://%s\n", panelAddress(cfg))
	fmt.Printf("inbounds: %d\nclients: %d\nadmins: %d\n", len(inbounds), len(clients), admins)
	if err := validateStoredInbounds(inbounds); err != nil {
		return err
	}
	if err := reportSingleUserViolations(inbounds, clients); err != nil {
		return err
	}
	return nil
}

// reportSingleUserViolations lists inbounds that break the one-enabled-client
// rule for methods without an identity header.
//
// The service rejects new writes that would create this state, but a database
// written by an older build can already contain it, and the runtime refuses to
// start such an inbound. `check` is the tool an operator runs before an upgrade,
// so it has to say which rows to fix instead of letting startup fail one by one.
func reportSingleUserViolations(inbounds []model.Inbound, clients []model.Client) error {
	enabledByInbound := make(map[int64][]string, len(inbounds))
	for _, c := range clients {
		if c.Enabled {
			enabledByInbound[c.InboundID] = append(enabledByInbound[c.InboundID], c.Name)
		}
	}
	var violations []string
	for _, in := range inbounds {
		if model.IsMultiUser(in.Method) {
			continue
		}
		if names := enabledByInbound[in.ID]; len(names) > 1 {
			violations = append(violations, fmt.Sprintf(
				"inbound %d (%s, %s) has %d enabled clients: %s",
				in.ID, in.Remark, in.Method, len(names), strings.Join(names, ", ")))
		}
	}
	if len(violations) == 0 {
		return nil
	}
	for _, line := range violations {
		fmt.Printf("warning: %s\n", line)
	}
	return fmt.Errorf("%d inbound(s) have more than one enabled client on a single-user method; disable all but one before starting", len(violations))
}

func validateStoredInbounds(inbounds []model.Inbound) error {
	for _, in := range inbounds {
		if err := ssserver.ValidateServerPSK(in.Method, in.ServerPSK); err != nil {
			return fmt.Errorf("inbound %d: %w", in.ID, err)
		}
	}
	return nil
}

func runAdmin(args []string) error {
	if len(args) == 0 {
		return errors.New("admin subcommand required: reset-password")
	}
	switch args[0] {
	case "reset-password":
		return adminResetPassword(args[1:])
	default:
		return fmt.Errorf("unknown admin subcommand %q", args[0])
	}
}

func adminResetPassword(args []string) error {
	fs, configPath, dbPath := newFlagSet("admin reset-password")
	username := fs.String("username", "admin", "administrator username")
	passwordFlag := fs.String("password", "", "new password (generated when empty)")
	cfg, err := loadConfig(fs, args, configPath, dbPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	admin, err := st.AdminByUsername(*username)
	if err != nil {
		return fmt.Errorf("administrator %q: %w", *username, err)
	}
	password := *passwordFlag
	if password == "" {
		generated, err := auth.RandomPassword(16)
		if err != nil {
			return err
		}
		password = generated
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := st.UpdateAdminPassword(admin.ID, hash, false); err != nil {
		return err
	}
	fmt.Printf("password updated for %s: %s\n", admin.Username, password)
	return nil
}

// runRepair checks the database and rebuilds the per-day traffic rollup.
//
// The rollup is maintained transactionally by normal writes, so rebuilding it is
// only needed after a bug or a manual edit left it out of step with the detail
// rows. RebuildDailyTotals existed as an unreachable "repair tool": no command
// or endpoint called it, which made an operational feature impossible to use
// exactly when it was needed.
//
// The integrity check runs first. A damaged database is not something a rollup
// rebuild can fix, and rewriting the summary table on top of one would only hide
// the real fault.
func runRepair(args []string) error {
	fs, configPath, dbPath := newFlagSet("repair")
	cfg, err := loadConfig(fs, args, configPath, dbPath)
	if err != nil {
		return err
	}
	// Repair must not fight the service for the write lock.
	st, err := store.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()

	status, err := st.IntegrityCheck()
	if err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	fmt.Printf("integrity: %s\n", status)
	if status != "ok" {
		return fmt.Errorf("database reports %q; back it up before doing anything else", status)
	}

	before, err := st.DailyTotalRows()
	if err != nil {
		return fmt.Errorf("daily totals: %w", err)
	}
	if err := st.RebuildDailyTotals(); err != nil {
		return fmt.Errorf("rebuild daily totals: %w", err)
	}
	after, err := st.DailyTotalRows()
	if err != nil {
		return fmt.Errorf("daily totals: %w", err)
	}
	fmt.Printf("daily totals rebuilt: %d -> %d rows\n", before, after)
	return nil
}

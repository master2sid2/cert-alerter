// Command cert-alerter monitors TLS certificates and e-mails expiry alerts
// to the recipients of the groups the certificates belong to.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // time zones for e-mail dates even on minimal images

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/auth"
	"github.com/master2sid2/cert-alerter/internal/config"
	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/logging"
	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/monitor"
	"github.com/master2sid2/cert-alerter/internal/secret"
	"github.com/master2sid2/cert-alerter/internal/server"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/version"
	"github.com/master2sid2/cert-alerter/web"
)

const usage = `cert-alerter — SSL/TLS certificate expiry monitoring with group based e-mail alerts.

Usage:
  cert-alerter [serve] [flags]        run the web server and the scheduler (default)
  cert-alerter migrate [flags]        apply database migrations and exit
  cert-alerter user create [flags]    create a user (e.g. a recovery admin)
  cert-alerter user set-password [flags]
                                      set the password of a user, unlock and activate it
  cert-alerter user list [flags]      list users
  cert-alerter db-copy [flags]        copy all data between databases (e.g. SQLite -> MySQL)
  cert-alerter healthcheck [-url U]   exit 0 when the local instance is healthy (for Docker)
  cert-alerter version                print the version

Run "cert-alerter <command> -h" for the flags of a command. Every flag can
also be set with an environment variable, see the flag help.
`

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "migrate":
		err = runMigrate(args)
	case "user":
		err = runUser(args)
	case "db-copy":
		err = runCopy(args)
	case "healthcheck":
		err = runHealthcheck(args)
	case "version":
		fmt.Println(version.String())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// newLogger opens the application log configured by -log-file. The caller
// closes outs when done.
func newLogger(cfg *config.Config, outs *logging.Outputs) (*slog.Logger, *logging.Output, error) {
	out, err := outs.Open(cfg.LogFile, logging.Stderr)
	if err != nil {
		return nil, nil, fmt.Errorf("application log: %w", err)
	}
	log, err := logging.NewAppLogger(out, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return nil, nil, err
	}
	return log, out, nil
}

func newFlags(name string, serve bool) (*config.Config, *flag.FlagSet) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfg := config.Register(fs)
	if serve {
		cfg.RegisterServe(fs)
	}
	return cfg, fs
}

func openDB(ctx context.Context, cfg *config.Config, log *slog.Logger) (*db.DB, error) {
	d, err := db.Open(ctx, cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		return nil, err
	}
	n, err := d.Migrate(ctx)
	if err != nil {
		d.Close()
		return nil, err
	}
	if n > 0 {
		log.Info("database migrated", "applied", n, "version", db.LatestVersion(d.Dialect))
	}
	return d, nil
}

func runServe(args []string) error {
	cfg, fs := newFlags("serve", true)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.Finalize(); err != nil {
		return err
	}
	outs := logging.NewOutputs()
	defer outs.Close()
	log, appOut, err := newLogger(cfg, outs)
	if err != nil {
		return err
	}
	access, accessOut, err := openAccessLog(cfg, outs)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reopenLogsOnHUP(ctx, outs, log)

	log.Info("starting cert-alerter", "version", version.String(), "db", cfg.DBDriver, "listen", cfg.Listen)
	log.Info("logging", "app_log", appOut.Name(), "access_log", accessOut.Name(), "access_log_format", cfg.AccessFormat)
	database, err := openDB(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer database.Close()

	key, keySource, err := secret.LoadKey(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("secret key: %w", err)
	}
	box, err := secret.NewBox(key)
	if err != nil {
		return err
	}
	log.Info("secret key loaded", "source", keySource)
	if cfg.SecretKey == "" && database.Dialect == db.MySQL {
		log.Warn("CERT_ALERTER_SECRET_KEY is not set; the SMTP password is encrypted with a key stored in the data directory, keep it next to the database or set the variable")
	}

	st := store.New(database)
	set := settings.NewService(st, box, cfg.BaseURL, log)
	if err := bootstrapAdmin(ctx, cfg, st, log); err != nil {
		return err
	}
	// Every e-mail (alerts, invites, resets, tests) is recorded in the mail journal.
	sender := &mailer.Journal{Next: mailer.SMTPSender{}, Store: st, Log: log}
	engine := alerts.New(st, set, sender, log)
	mon := monitor.New(ctx, st, set, engine, log, cfg.InstanceID, cfg.Scheduler)
	if cfg.CAFile != "" {
		roots, err := loadRoots(cfg.CAFile)
		if err != nil {
			return err
		}
		mon.Roots = roots
		log.Info("additional trusted CA certificates loaded", "file", cfg.CAFile)
	}
	srv, err := server.New(ctx, server.Deps{
		Config: cfg, Store: st, Settings: set, Monitor: mon, Alerts: engine, Sender: sender,
		Log: log, AccessLog: access, Static: web.Static(), Version: version.String(),
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	if token := srv.SetupToken(); token != "" {
		printSetupBanner(cfg, ln.Addr(), token)
	}

	go mon.Run(ctx)
	errc := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" {
			errc <- httpSrv.ServeTLS(ln, cfg.TLSCert, cfg.TLSKey)
		} else {
			errc <- httpSrv.Serve(ln)
		}
	}()
	log.Info("listening", "addr", ln.Addr().String(), "tls", cfg.TLSCert != "")

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	stop()
	mon.Wait()
	return nil
}

// openAccessLog opens the HTTP access log; it returns a nil *AccessLog when
// the access log is switched off.
func openAccessLog(cfg *config.Config, outs *logging.Outputs) (*logging.AccessLog, *logging.Output, error) {
	out, err := outs.Open(cfg.AccessLog, logging.Stdout)
	if err != nil {
		return nil, nil, fmt.Errorf("access log: %w", err)
	}
	access, err := logging.NewAccessLog(out, cfg.AccessFormat)
	if err != nil {
		return nil, nil, err
	}
	if !out.Enabled() {
		access = nil
	}
	return access, out, nil
}

// reopenLogsOnHUP reopens the log files on SIGHUP so that logrotate (or a
// similar tool) can move them away without restarting the process.
func reopenLogsOnHUP(ctx context.Context, outs *logging.Outputs, log *slog.Logger) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			if err := outs.Reopen(); err != nil {
				log.Error("reopen log files", "err", err)
			} else {
				log.Info("log files reopened")
			}
		}
	}
}

func printSetupBanner(cfg *config.Config, addr net.Addr, token string) {
	base := cfg.BaseURL
	if base == "" {
		scheme := "http"
		if cfg.TLSCert != "" {
			scheme = "https"
		}
		host, port := "localhost", "8080"
		if tcp, ok := addr.(*net.TCPAddr); ok {
			if !tcp.IP.IsUnspecified() && !tcp.IP.IsLoopback() {
				host = tcp.IP.String()
			}
			port = strconv.Itoa(tcp.Port)
		}
		base = scheme + "://" + net.JoinHostPort(host, port)
	}
	line := strings.Repeat("=", 78)
	fmt.Fprintf(os.Stderr, "\n%s\n  First start: no users exist yet. Create the administrator account at\n\n    %s/setup?token=%s\n\n  (the link stays valid until the first administrator is created)\n%s\n\n",
		line, base, token, line)
}

// loadRoots returns the system trust store extended with the PEM file.
func loadRoots(file string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no PEM certificates found in %s", file)
	}
	return pool, nil
}

// bootstrapAdmin creates the configured admin when the database is empty.
func bootstrapAdmin(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) error {
	if cfg.AdminEmail == "" && cfg.AdminPassword == "" {
		return nil
	}
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		return errors.New("both admin-email and admin-password are required to bootstrap an admin")
	}
	n, err := st.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	if len(cfg.AdminPassword) < 8 {
		return errors.New("the bootstrap admin password must be at least 8 characters long")
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	u := &store.User{Email: cfg.AdminEmail, Name: "Administrator", Role: store.RoleAdmin, Status: store.UserActive,
		PasswordHash: hash, PasswordChangedAt: &now}
	if err := st.CreateUser(ctx, u); err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}
	log.Info("bootstrap administrator created", "email", u.Email)
	return nil
}

func runMigrate(args []string) error {
	cfg, fs := newFlags("migrate", false)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.Finalize(); err != nil {
		return err
	}
	outs := logging.NewOutputs()
	defer outs.Close()
	log, _, err := newLogger(cfg, outs)
	if err != nil {
		return err
	}
	d, err := openDB(context.Background(), cfg, log)
	if err != nil {
		return err
	}
	defer d.Close()
	fmt.Printf("database schema is at version %d\n", db.LatestVersion(d.Dialect))
	return nil
}

func runUser(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cert-alerter user create|set-password|list [flags]")
	}
	sub, args := args[0], args[1:]
	cfg, fs := newFlags("user "+sub, false)
	email := fs.String("email", "", "user e-mail")
	name := fs.String("name", "", "display name (create)")
	role := fs.String("role", store.RoleAdmin, "role for create: admin or user")
	password := fs.String("password", "", "password (default: read from stdin with -password-stdin, or generate)")
	fromStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.Finalize(); err != nil {
		return err
	}
	ctx := context.Background()
	outs := logging.NewOutputs()
	defer outs.Close()
	log, _, err := newLogger(cfg, outs)
	if err != nil {
		return err
	}
	d, err := openDB(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer d.Close()
	st := store.New(d)

	if sub == "list" {
		users, err := st.ListUsers(ctx)
		if err != nil {
			return err
		}
		for _, u := range users {
			fmt.Printf("%-5d %-40s %-6s %-9s %s\n", u.ID, u.Email, u.Role, u.Status, u.Name)
		}
		return nil
	}
	if *email == "" {
		return errors.New("-email is required")
	}
	pw, generated, err := readPassword(*password, *fromStdin, os.Stdin)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	switch sub {
	case "create":
		if *role != store.RoleAdmin && *role != store.RoleUser {
			return fmt.Errorf("invalid role %q", *role)
		}
		now := time.Now().UTC()
		u := &store.User{Email: *email, Name: *name, Role: *role, Status: store.UserActive, PasswordHash: hash,
			PasswordChangedAt: &now}
		if err := st.CreateUser(ctx, u); err != nil {
			return err
		}
		fmt.Printf("user %s created (role %s)\n", u.Email, u.Role)
	case "set-password":
		u, err := st.GetUserByEmail(ctx, *email)
		if err != nil {
			return fmt.Errorf("user %s: %w", *email, err)
		}
		if err := st.SetUserPassword(ctx, u.ID, hash, true, 24); err != nil {
			return err
		}
		if u.Status == store.UserDisabled {
			if err := st.UpdateUserAccess(ctx, u.ID, u.Name, u.Role, store.UserActive); err != nil {
				return err
			}
		}
		_ = st.DeleteUserSessions(ctx, u.ID, "")
		fmt.Printf("password of %s updated, account active and unlocked\n", u.Email)
	default:
		return fmt.Errorf("unknown user command %q", sub)
	}
	if generated {
		fmt.Printf("generated password: %s\n", pw)
	}
	return nil
}

func readPassword(flagValue string, fromStdin bool, stdin io.Reader) (string, bool, error) {
	switch {
	case flagValue != "":
		return flagValue, false, nil
	case fromStdin:
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return "", false, errors.New("empty password on stdin")
		}
		return line, false, nil
	}
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	// Guarantee all character classes required by the default policy.
	return string(buf) + "-7Aa", true, nil
}

func runHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	listen := os.Getenv("CERT_ALERTER_LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	host, port, _ := net.SplitHostPort(listen)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if os.Getenv("CERT_ALERTER_TLS_CERT") != "" {
		scheme = "https"
	}
	url := fs.String("url", scheme+"://"+net.JoinHostPort(host, port)+"/healthz", "health endpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		// The health check only needs to reach the local process.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // local liveness probe
	}}
	res, err := client.Get(*url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: HTTP %d", res.StatusCode)
	}
	return nil
}

func runCopy(args []string) error {
	fs := flag.NewFlagSet("db-copy", flag.ContinueOnError)
	fromDriver := fs.String("from-driver", "sqlite", "source driver: sqlite or mysql")
	fromDSN := fs.String("from-dsn", "data/cert-alerter.db", "source DSN")
	toDriver := fs.String("to-driver", "mysql", "target driver: sqlite or mysql")
	toDSN := fs.String("to-dsn", "", "target DSN, e.g. user:pass@tcp(127.0.0.1:3306)/cert_alerter")
	force := fs.Bool("force", false, "delete existing rows in the target first")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Copy all data from one database to another (schema is created automatically).")
		fmt.Fprintln(fs.Output(), "Example: cert-alerter db-copy -from-dsn data/cert-alerter.db -to-driver mysql -to-dsn 'app:secret@tcp(db:3306)/cert_alerter'")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *toDSN == "" {
		return errors.New("-to-dsn is required")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	src, err := openDB(ctx, &config.Config{DBDriver: *fromDriver, DBDSN: *fromDSN}, log)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer src.Close()
	dst, err := openDB(ctx, &config.Config{DBDriver: *toDriver, DBDSN: *toDSN}, log)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	defer dst.Close()
	total := 0
	err = db.CopyData(ctx, src, dst, *force, func(s db.CopyStats) {
		total += s.Rows
		fmt.Printf("  %-20s %6d rows\n", s.Table, s.Rows)
	})
	if err != nil {
		return err
	}
	fmt.Printf("done: %d rows copied from %s to %s\n", total, src.Dialect, dst.Dialect)
	fmt.Println("Keep the same CERT_ALERTER_SECRET_KEY (or copy data/secret.key) so the stored SMTP password can be decrypted.")
	return nil
}

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"reproq-worker/internal/config"
	"reproq-worker/internal/db"
	"reproq-worker/internal/events"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/logging"
	"reproq-worker/internal/metrics"
	"reproq-worker/internal/queue"
	"reproq-worker/internal/runner"
	"reproq-worker/internal/web"
)

const Version = "0.0.140"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	if os.Args[1] == "--version" || os.Args[1] == "version" {
		fmt.Printf("reproq-worker version %s\n", Version)
		return
	}

	switch os.Args[1] {
	case "worker":
		runWorker(os.Args[2:])
	case "beat":
		runBeat(os.Args[2:])
	case "replay":
		runReplay(os.Args[2:])
	case "limit":
		runLimit(os.Args[2:])
	case "cancel":
		runCancel(os.Args[2:])
	case "triage":
		runTriage(os.Args[2:])
	case "prune":
		runPrune(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("usage: reproq <worker|beat|replay|limit|cancel|triage|prune|version> [args]")
}

type metricsConfig struct {
	addr           string
	port           int
	authToken      string
	allowCIDRs     string
	authLimit      int
	authWindow     time.Duration
	authMaxEntries int
	tlsCert        string
	tlsKey         string
	tlsClientCA    string
}

func defaultMetricsConfig() metricsConfig {
	return metricsConfig{
		authLimit:      web.DefaultAuthLimit,
		authWindow:     web.DefaultAuthWindow,
		authMaxEntries: web.DefaultAuthMaxEntries,
	}
}

func isLowMemoryMode() bool {
	return parseTruthy(os.Getenv("LOW_MEMORY_MODE"))
}

func parseTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func applyMetricsFileConfig(cfg *metricsConfig, fileCfg *config.FileConfig) error {
	if fileCfg == nil {
		return nil
	}
	metrics := fileCfg.Metrics
	if metrics.Addr != "" {
		cfg.addr = metrics.Addr
	}
	if metrics.Port != nil {
		cfg.port = *metrics.Port
	}
	if metrics.AuthToken != "" {
		cfg.authToken = metrics.AuthToken
	}
	if len(metrics.AllowCIDRs) > 0 {
		cfg.allowCIDRs = strings.Join(metrics.AllowCIDRs, ",")
	}
	if metrics.AuthLimit != nil {
		cfg.authLimit = *metrics.AuthLimit
	}
	if metrics.AuthWindow != "" {
		parsed, err := time.ParseDuration(metrics.AuthWindow)
		if err != nil || parsed <= 0 {
			if err == nil {
				err = fmt.Errorf("must be a positive duration")
			}
			return fmt.Errorf("invalid metrics.auth_window: %w", err)
		}
		cfg.authWindow = parsed
	}
	if metrics.AuthMaxEntries != nil {
		cfg.authMaxEntries = *metrics.AuthMaxEntries
	}
	if metrics.TLSCert != "" {
		cfg.tlsCert = metrics.TLSCert
	}
	if metrics.TLSKey != "" {
		cfg.tlsKey = metrics.TLSKey
	}
	if metrics.TLSClientCA != "" {
		cfg.tlsClientCA = metrics.TLSClientCA
	}
	return nil
}

func applyMetricsEnv(cfg *metricsConfig) error {
	if val := os.Getenv("METRICS_ADDR"); val != "" {
		cfg.addr = val
	}
	if val := os.Getenv("METRICS_AUTH_TOKEN"); val != "" {
		cfg.authToken = val
	}
	if val := os.Getenv("METRICS_ALLOW_CIDRS"); val != "" {
		cfg.allowCIDRs = val
	}
	if val := os.Getenv("METRICS_AUTH_LIMIT"); val != "" {
		parsed, err := strconv.Atoi(val)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid METRICS_AUTH_LIMIT (must be a positive integer)")
		}
		cfg.authLimit = parsed
	}
	if val := os.Getenv("METRICS_AUTH_WINDOW"); val != "" {
		parsed, err := time.ParseDuration(val)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid METRICS_AUTH_WINDOW (must be a positive duration, e.g. 1m)")
		}
		cfg.authWindow = parsed
	}
	if val := os.Getenv("METRICS_AUTH_MAX_ENTRIES"); val != "" {
		parsed, err := strconv.Atoi(val)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid METRICS_AUTH_MAX_ENTRIES (must be a positive integer)")
		}
		cfg.authMaxEntries = parsed
	}
	if val := os.Getenv("METRICS_TLS_CERT"); val != "" {
		cfg.tlsCert = val
	}
	if val := os.Getenv("METRICS_TLS_KEY"); val != "" {
		cfg.tlsKey = val
	}
	if val := os.Getenv("METRICS_TLS_CLIENT_CA"); val != "" {
		cfg.tlsClientCA = val
	}
	return nil
}

func runWorker(args []string) {
	configPath, err := config.ResolveConfigPath(args)
	if err != nil {
		log.Fatal(err)
	}
	fileCfg, err := config.LoadFileConfig(configPath)
	if err != nil {
		log.Fatal(err)
	}

	cfg := config.DefaultConfig()
	if err := config.ApplyFileConfig(cfg, fileCfg); err != nil {
		log.Fatal(err)
	}
	if err := config.ApplyEnv(cfg); err != nil {
		log.Fatal(err)
	}

	metricsCfg := defaultMetricsConfig()
	if err := applyMetricsFileConfig(&metricsCfg, fileCfg); err != nil {
		log.Fatal(err)
	}
	if err := applyMetricsEnv(&metricsCfg); err != nil {
		log.Fatal(err)
	}

	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	fs.String("config", configPath, "Path to reproq config file")
	metricsPort := fs.Int("metrics-port", metricsCfg.port, "Port to serve Prometheus metrics (0 to disable)")
	metricsAddr := fs.String("metrics-addr", metricsCfg.addr, "Address to serve health/metrics (overrides --metrics-port)")
	metricsToken := fs.String("metrics-auth-token", metricsCfg.authToken, "Bearer token required for /metrics and /healthz")
	metricsAllowCIDRs := fs.String("metrics-allow-cidrs", metricsCfg.allowCIDRs, "Comma-separated IP/CIDR allow-list for /metrics and /healthz")
	metricsAuthLimit := fs.Int("metrics-auth-limit", metricsCfg.authLimit, "Unauthorized request limit per window")
	metricsAuthWindow := fs.Duration("metrics-auth-window", metricsCfg.authWindow, "Window for unauthorized request rate limiting")
	metricsAuthMaxEntries := fs.Int("metrics-auth-max-entries", metricsCfg.authMaxEntries, "Max tracked hosts for auth rate limiting")
	metricsTLSCert := fs.String("metrics-tls-cert", metricsCfg.tlsCert, "TLS certificate path for health/metrics")
	metricsTLSKey := fs.String("metrics-tls-key", metricsCfg.tlsKey, "TLS private key path for health/metrics")
	metricsTLSClientCA := fs.String("metrics-tls-client-ca", metricsCfg.tlsClientCA, "Optional client CA bundle for mTLS on health/metrics")
	cfg.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if cfg.DatabaseURL == "" {
		log.Fatal("DSN required (use --dsn, DATABASE_URL, or config file)")
	}

	cfg.Version = Version

	logger := logging.Init(cfg.WorkerID)
	memoryLogInterval := memoryLogIntervalFromEnv(logger)
	lowMemory := isLowMemoryMode()
	if lowMemory {
		logger.Warn("Low memory mode enabled; metrics/health/events disabled", "env", "LOW_MEMORY_MODE")
	}
	if cfg.PayloadMode == "inline" {
		if config.ProductionBuild {
			logger.Error("Payload mode inline disabled in production builds; use stdin or file", "payload_mode", cfg.PayloadMode)
			os.Exit(1)
		}
		logger.Warn("Payload mode inline exposes payload in process args; prefer stdin or file", "payload_mode", cfg.PayloadMode)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	startMemoryLogger(ctx, logger, memoryLogInterval)

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	addr := ""
	var broker *events.Broker
	if *metricsAddr != "" {
		addr = *metricsAddr
	} else if *metricsPort > 0 {
		addr = fmt.Sprintf(":%d", *metricsPort)
	}
	if addr != "" {
		if lowMemory {
			logger.Warn("Skipping metrics server due to low memory mode", "addr", addr)
		} else {
			broker = events.NewBroker(200)
			allowlist, err := web.ParseCIDRAllowlist(*metricsAllowCIDRs)
			if err != nil {
				log.Fatal(err)
			}
			if *metricsAuthLimit <= 0 {
				log.Fatal("--metrics-auth-limit must be a positive integer")
			}
			if *metricsAuthWindow <= 0 {
				log.Fatal("--metrics-auth-window must be a positive duration")
			}
			if *metricsAuthMaxEntries <= 0 {
				log.Fatal("--metrics-auth-max-entries must be a positive integer")
			}
			tlsConfig, err := web.BuildTLSConfig(*metricsTLSCert, *metricsTLSKey, *metricsTLSClientCA)
			if err != nil {
				log.Fatal(err)
			}
			clientAuth := tlsConfig != nil && tlsConfig.ClientAuth == tls.RequireAndVerifyClientCert
			if *metricsToken == "" && !isLoopbackAddr(addr) && allowlist == nil && !clientAuth {
				logger.Warn("Metrics endpoint has no auth; bind to localhost or set --metrics-auth-token", "addr", addr)
			}
			server := web.NewServer(pool, addr, *metricsToken, *metricsToken, *metricsAuthLimit, *metricsAuthWindow, *metricsAuthMaxEntries, allowlist, tlsConfig, broker)
			go func() {
				logger.Info("Serving health and metrics", "addr", addr)
				if err := server.Start(ctx); err != nil && err != http.ErrServerClosed {
					logger.Error("Metrics server error", "error", err)
				}
			}()
			metrics.StartCollector(ctx, pool, 5*time.Second, logger)
		}
	}

	q := queue.NewService(pool)
	exec := &executor.ShellExecutor{
		PythonBin:       cfg.PythonBin,
		ExecutorModule:  cfg.ExecutorModule,
		PayloadMode:     cfg.PayloadMode,
		MaxPayloadBytes: cfg.MaxPayloadBytes,
		MaxStdoutBytes:  cfg.MaxStdoutBytes,
		MaxStderrBytes:  cfg.MaxStderrBytes,
	}

	var publisher events.Publisher = events.NoopPublisher{}
	if broker != nil {
		publisher = broker
	}
	r := runner.New(cfg, q, exec, logger, publisher)
	if err := r.Start(ctx); err != nil {
		log.Fatal(err)
	}
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func runBeat(args []string) {
	configPath, err := config.ResolveConfigPath(args)
	if err != nil {
		log.Fatal(err)
	}
	fileCfg, err := config.LoadFileConfig(configPath)
	if err != nil {
		log.Fatal(err)
	}

	beatDSN := ""
	beatInterval := 30 * time.Second
	if fileCfg != nil {
		if fileCfg.DSN != "" {
			beatDSN = fileCfg.DSN
		}
		if fileCfg.Beat.DSN != "" {
			beatDSN = fileCfg.Beat.DSN
		}
		if fileCfg.Beat.Interval != "" {
			parsed, err := time.ParseDuration(fileCfg.Beat.Interval)
			if err != nil || parsed <= 0 {
				if err == nil {
					err = fmt.Errorf("must be a positive duration")
				}
				log.Fatalf("invalid beat.interval: %v", err)
			}
			beatInterval = parsed
		}
	}
	if val := os.Getenv("DATABASE_URL"); val != "" {
		beatDSN = val
	}

	fs := flag.NewFlagSet("beat", flag.ExitOnError)
	fs.String("config", configPath, "Path to reproq config file")
	dsn := fs.String("dsn", beatDSN, "Postgres DSN")
	interval := fs.Duration("interval", beatInterval, "Polling interval for periodic tasks")
	once := fs.Bool("once", false, "Enqueue due tasks once and exit")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *dsn == "" {
		log.Fatal("DSN required (use --dsn, DATABASE_URL, or config file)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	fmt.Printf("Starting reproq beat (interval: %v)...\n", *interval)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	// Run once immediately
	if n, err := q.EnqueueDuePeriodicTasks(ctx); err != nil {
		fmt.Printf("Error: %v\n", err)
	} else if n > 0 {
		fmt.Printf("Enqueued %d periodic tasks\n", n)
	}
	if *once {
		fmt.Println("Beat run complete (--once).")
		return
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Shutting down beat...")
			return
		case <-ticker.C:
			if n, err := q.EnqueueDuePeriodicTasks(ctx); err != nil {
				fmt.Printf("Error: %v\n", err)
			} else if n > 0 {
				fmt.Printf("Enqueued %d periodic tasks\n", n)
			}
		}
	}
}

func runReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	id := fs.Int64("id", 0, "Result ID to replay")
	specHash := fs.String("spec-hash", "", "Spec hash to replay (uses latest match)")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *dsn == "" {
		log.Fatal("DSN required")
	}
	if (*id == 0 && *specHash == "") || (*id != 0 && *specHash != "") {
		log.Fatal("Provide exactly one of --id or --spec-hash")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	if *specHash != "" {
		sourceID, newID, err := q.ReplayBySpecHash(ctx, *specHash)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Requeued task %d (spec_hash %s) as new result_id %d\n", sourceID, *specHash, newID)
		return
	}

	newID, err := q.Replay(ctx, *id)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Requeued task %d as new result_id %d\n", *id, newID)
}

func runLimit(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: reproq limit <set|ls|rm> [args]")
		return
	}

	switch args[0] {
	case "set":
		fs := flag.NewFlagSet("limit set", flag.ExitOnError)
		dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
		key := fs.String("key", "", "Rate limit key (queue:<name> | task:<path> | global)")
		rate := fs.Float64("rate", 0, "Tokens per second")
		burst := fs.Int("burst", 1, "Burst size")
		if err := fs.Parse(args[1:]); err != nil {
			log.Fatal(err)
		}

		if *dsn == "" || *key == "" {
			log.Fatal("DSN and --key required")
		}

		ctx := context.Background()
		pool, err := db.NewPool(ctx, *dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()

		q := queue.NewService(pool)
		if err := q.SetRateLimit(ctx, *key, *rate, *burst); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Set rate limit %s: %0.2f tokens/sec, burst %d\n", *key, *rate, *burst)
	case "ls":
		fs := flag.NewFlagSet("limit ls", flag.ExitOnError)
		dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
		if err := fs.Parse(args[1:]); err != nil {
			log.Fatal(err)
		}

		if *dsn == "" {
			log.Fatal("DSN required")
		}

		ctx := context.Background()
		pool, err := db.NewPool(ctx, *dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()

		q := queue.NewService(pool)
		limits, err := q.ListRateLimits(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if len(limits) == 0 {
			fmt.Println("No rate limits configured.")
			return
		}
		fmt.Println("Key\tTokens/s\tBurst\tCurrent\tLastRefill")
		for _, rl := range limits {
			fmt.Printf("%s\t%0.2f\t%d\t%0.2f\t%s\n", rl.Key, rl.TokensPerSec, rl.BurstSize, rl.CurrentTokens, rl.LastRefilledAt.Format(time.RFC3339))
		}
	case "rm":
		fs := flag.NewFlagSet("limit rm", flag.ExitOnError)
		dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
		key := fs.String("key", "", "Rate limit key to delete")
		if err := fs.Parse(args[1:]); err != nil {
			log.Fatal(err)
		}

		if *dsn == "" || *key == "" {
			log.Fatal("DSN and --key required")
		}

		ctx := context.Background()
		pool, err := db.NewPool(ctx, *dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()

		q := queue.NewService(pool)
		deleted, err := q.DeleteRateLimit(ctx, *key)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Deleted %d rate limit(s) for %s\n", deleted, *key)
	default:
		fmt.Println("usage: reproq limit <set|ls|rm> [args]")
	}
}

func runPrune(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: reproq prune <expired> [args]")
		return
	}

	switch args[0] {
	case "expired":
		fs := flag.NewFlagSet("prune expired", flag.ExitOnError)
		dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
		limit := fs.Int("limit", 0, "Max tasks to delete (0 = no limit)")
		dryRun := fs.Bool("dry-run", false, "Show count without deleting")
		if err := fs.Parse(args[1:]); err != nil {
			log.Fatal(err)
		}

		if *dsn == "" {
			log.Fatal("DSN required")
		}

		ctx := context.Background()
		pool, err := db.NewPool(ctx, *dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()

		q := queue.NewService(pool)
		count, err := q.PruneExpired(ctx, *limit, *dryRun)
		if err != nil {
			log.Fatal(err)
		}
		if *dryRun {
			fmt.Printf("Would prune %d expired task(s)\n", count)
			return
		}
		fmt.Printf("Pruned %d expired task(s)\n", count)
	default:
		fmt.Println("usage: reproq prune <expired> [args]")
	}
}

func runCancel(args []string) {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	id := fs.Int64("id", 0, "Result ID to cancel")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *dsn == "" || *id == 0 {
		log.Fatal("DSN and --id required")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	updated, err := q.RequestCancel(ctx, *id)
	if err != nil {
		log.Fatal(err)
	}
	if updated == 0 {
		fmt.Printf("No RUNNING task found for result_id %d\n", *id)
		return
	}
	fmt.Printf("Cancellation requested for task %d\n", *id)
}

func runTriage(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: reproq triage <list|inspect|retry> [args]")
		return
	}

	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("triage list", flag.ExitOnError)
		dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
		limit := fs.Int("limit", 50, "Max tasks to list")
		queueName := fs.String("queue", "", "Filter by queue name")
		if err := fs.Parse(args[1:]); err != nil {
			log.Fatal(err)
		}

		if *dsn == "" {
			log.Fatal("DSN required")
		}

		ctx := context.Background()
		pool, err := db.NewPool(ctx, *dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()

		q := queue.NewService(pool)
		items, err := q.ListFailedTasks(ctx, *limit, *queueName)
		if err != nil {
			log.Fatal(err)
		}
		if len(items) == 0 {
			fmt.Println("No failed tasks.")
			return
		}
		fmt.Println("ID\tQueue\tTask\tAttempts\tFailedAt\tLastError")
		for _, item := range items {
			failedAt := ""
			if item.FailedAt != nil {
				failedAt = item.FailedAt.Format(time.RFC3339)
			}
			lastError := ""
			if item.LastError != nil {
				lastError = *item.LastError
			}
			fmt.Printf("%d\t%s\t%s\t%d/%d\t%s\t%s\n", item.ResultID, item.QueueName, item.TaskPath, item.Attempts, item.MaxAttempts, failedAt, lastError)
		}
	case "inspect":
		fs := flag.NewFlagSet("triage inspect", flag.ExitOnError)
		dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
		id := fs.Int64("id", 0, "Result ID to inspect")
		if err := fs.Parse(args[1:]); err != nil {
			log.Fatal(err)
		}

		if *dsn == "" || *id == 0 {
			log.Fatal("DSN and --id required")
		}

		ctx := context.Background()
		pool, err := db.NewPool(ctx, *dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()

		q := queue.NewService(pool)
		item, err := q.InspectFailedTask(ctx, *id)
		if err != nil {
			log.Fatal(err)
		}

		failedAt := ""
		if item.FailedAt != nil {
			failedAt = item.FailedAt.Format(time.RFC3339)
		}
		lastError := ""
		if item.LastError != nil {
			lastError = *item.LastError
		}

		fmt.Printf("Result ID: %d\n", item.ResultID)
		fmt.Printf("Queue: %s\n", item.QueueName)
		fmt.Printf("Task Path: %s\n", item.TaskPath)
		fmt.Printf("Status: %s\n", item.Status)
		fmt.Printf("Attempts: %d/%d\n", item.Attempts, item.MaxAttempts)
		fmt.Printf("Failed At: %s\n", failedAt)
		fmt.Printf("Last Error: %s\n", lastError)
		fmt.Printf("Spec JSON: %s\n", string(item.SpecJSON))
		fmt.Printf("Errors JSON: %s\n", string(item.ErrorsJSON))
	case "retry":
		fs := flag.NewFlagSet("triage retry", flag.ExitOnError)
		dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
		id := fs.Int64("id", 0, "Result ID to retry")
		all := fs.Bool("all", false, "Retry all failed tasks")
		if err := fs.Parse(args[1:]); err != nil {
			log.Fatal(err)
		}

		if *dsn == "" {
			log.Fatal("DSN required")
		}
		if *id == 0 && !*all {
			log.Fatal("Provide --id or --all")
		}

		ctx := context.Background()
		pool, err := db.NewPool(ctx, *dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()

		q := queue.NewService(pool)
		var updated int64
		if *all {
			updated, err = q.RetryAllFailedTasks(ctx)
		} else {
			updated, err = q.RetryFailedTask(ctx, *id)
		}
		if err != nil {
			log.Fatal(err)
		}
		if updated == 0 {
			fmt.Println("No failed tasks updated.")
			return
		}
		fmt.Printf("Retried %d task(s)\n", updated)
	default:
		fmt.Println("usage: reproq triage <list|inspect|retry> [args]")
	}
}

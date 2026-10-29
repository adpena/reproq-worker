package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"reproq-worker/internal/config"
	"reproq-worker/internal/db"
	"reproq-worker/internal/protocol"
	"reproq-worker/internal/queue"
	"reproq-worker/internal/testdatabase"
)

func TestMain(m *testing.M) {
	switch os.Getenv("REPROQ_CLI_TEST_COMMAND") {
	case "reclaim":
		runReclaim(os.Args[1:])
		os.Exit(0)
	case "prune":
		runPrune(os.Args[1:])
		os.Exit(0)
	case "worker":
		runWorker(os.Args[1:])
		os.Exit(0)
	case "beat":
		runBeat(os.Args[1:])
		os.Exit(0)
	case "capabilities":
		runCapabilities()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWorkerCapabilityCommandUsesRuntimeProtocolContract(t *testing.T) {
	output, err := cliChild(t, "capabilities", "")
	if err != nil {
		t.Fatalf("capability command failed: %s", output)
	}
	var capability protocol.Capabilities
	if err := json.Unmarshal(output, &capability); err != nil || capability.WorkerVersion != Version || capability.SchemaID != protocol.SchemaID || capability.SchemaVersion != protocol.SchemaVersion || capability.ExecutorProtocol != protocol.ExecutorVersion || capability.SDKVersion != "" {
		t.Fatal("worker capability response disagrees with runtime")
	}
	if capability.Operations["history_prune"] != 1 {
		t.Fatal("worker did not advertise canonical history pruning")
	}
}

func cliChild(t *testing.T, commandName, dsn string, arguments ...string) ([]byte, error) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, config.QueueDatabaseEnv+"=") && !strings.HasPrefix(entry, "REPROQ_CLI_TEST_COMMAND=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "REPROQ_CLI_TEST_COMMAND="+commandName, config.QueueDatabaseEnv+"="+dsn, "REPROQ_CONFIG=")
	return command.CombinedOutput()
}

func TestReclaimCLIRejectsInvalidArgumentsBeforeOpeningDatabase(t *testing.T) {
	for _, arguments := range [][]string{{"--action", "unknown"}, {"--older-than", "-1s"}, {"--limit", "-1"}, {"unexpected"}} {
		output, err := cliChild(t, "reclaim", "postgres://owned@127.0.0.1:1/reproq_test_never_contact", arguments...)
		if err == nil || !strings.Contains(string(output), "reclaim requires") || strings.Contains(string(output), "failed to") {
			t.Fatalf("invalid command reached a database or succeeded: %s", output)
		}
	}
}

func TestPruneCLIRejectsNegativeLimitBeforeOpeningDatabase(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		arguments := []string{"expired", "--limit", "-1"}
		if dryRun {
			arguments = append(arguments, "--dry-run")
		}
		output, err := cliChild(t, "prune", "postgres://owned@127.0.0.1:1/reproq_test_never_contact", arguments...)
		if err == nil || !strings.Contains(string(output), queue.ErrInvalidPruneLimit.Error()) || strings.Contains(string(output), "failed to") {
			t.Fatalf("negative prune limit reached a database or succeeded: %s", output)
		}
	}
}

func TestReclaimCLIIntegration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://owned-business@127.0.0.1:1/business_database_must_not_be_used")
	dsn := testdatabase.URL(t)
	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service, err := queue.NewService(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("cli-reclaim-%d", time.Now().UnixNano())
	defer pool.Exec(ctx, "DELETE FROM public.task_runs WHERE queue_name=$1", name)
	ids := []int64{}
	for ordinal := range 3 {
		var id int64
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(name, ordinal))))
		if err := pool.QueryRow(ctx, `INSERT INTO public.task_runs(spec_version,spec_hash,queue_name,spec_json) VALUES(1,$1,$2,'{"task_path":"owned.cli.task"}') RETURNING result_id`, hash, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		task, err := service.Claim(ctx, "cli-worker", name, 60, 0)
		if err != nil || task.ResultID != id {
			t.Fatal("cannot prepare exact owned CLI claim:", err)
		}
		ids = append(ids, id)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()-interval '2 hours' WHERE result_id=ANY($1::bigint[])", ids[:2]); err != nil {
		t.Fatal(err)
	}
	output, err := cliChild(t, "reclaim", dsn, "--older-than", "1h", "--limit", "1", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("actual reclaim dry run failed: %s", output)
	}
	var summary queue.ReclaimResult
	if err := json.Unmarshal(output, &summary); err != nil || summary.Count != 1 || !summary.DryRun {
		t.Fatalf("invalid dry-run protocol: %s", output)
	}
	var running int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.task_runs WHERE result_id=ANY($1::bigint[]) AND status='RUNNING' AND lease_token IS NOT NULL", ids).Scan(&running); err != nil || running != 3 {
		t.Fatal("dry run mutated a lease")
	}
	output, err = cliChild(t, "reclaim", dsn, "--older-than", "1h", "--limit", "1", "--action", "fail", "--json")
	if err != nil {
		t.Fatalf("actual reclaim failed: %s", output)
	}
	if err := json.Unmarshal(output, &summary); err != nil || summary.Count != 1 || summary.Failed != 1 || summary.DryRun {
		t.Fatalf("invalid actual reclaim summary: %s", output)
	}
	var futureUntouched bool
	if err := pool.QueryRow(ctx, "SELECT status='RUNNING' AND lease_token IS NOT NULL AND leased_until>clock_timestamp() FROM public.task_runs WHERE result_id=$1", ids[2]).Scan(&futureUntouched); err != nil || !futureUntouched {
		t.Fatal("limited reclaim touched a live lease")
	}
}

func TestCLIHelpAndFlagErrorsNeverPrintConfiguredCredentials(t *testing.T) {
	privateDSN := "postgres://owned:OWNED_DSN_SECRET_CANARY@127.0.0.1/reproq_test_no_connect"
	privateToken := "OWNED_METRICS_SECRET_CANARY"
	t.Setenv("METRICS_AUTH_TOKEN", privateToken)
	for _, command := range []string{"reclaim", "worker"} {
		for _, argument := range []string{"--help", "--unknown-flag"} {
			output, err := cliChild(t, command, privateDSN, argument)
			if argument == "--help" && err != nil {
				t.Fatalf("help failed: %s", output)
			}
			if argument == "--unknown-flag" && err == nil {
				t.Fatal("unknown flag accepted")
			}
			if strings.Contains(string(output), privateDSN) || strings.Contains(string(output), "OWNED_DSN_SECRET_CANARY") || strings.Contains(string(output), privateToken) {
				t.Fatal("CLI usage exposed configured credentials")
			}
			if !strings.Contains(string(output), "dsn") {
				t.Fatal("command help omitted DSN configuration")
			}
		}
	}
}

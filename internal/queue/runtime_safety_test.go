package queue

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reproq-worker/internal/testdatabase"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClaimNeverExceedsStoredAttemptBudget(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), testdatabase.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	for _, aging := range []float64{0, 60} {
		queueName := fmt.Sprintf("attempt-budget-%d-%g", time.Now().UnixNano(), aging)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(queueName)))
		var id int64
		err := pool.QueryRow(ctx, `INSERT INTO task_runs(spec_version,spec_hash,queue_name,spec_json,status,attempts,max_attempts)
			VALUES (1,$1,$2,'{"task_path":"tasks.test"}','READY',3,3) RETURNING result_id`, hash, queueName).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(ctx, "DELETE FROM task_runs WHERE result_id=$1", id)
		if _, err := newTestService(t, pool).Claim(ctx, "budget-test-worker", queueName, 60, aging); !errors.Is(err, ErrNoTasks) {
			t.Fatalf("exhausted task claimed: %v", err)
		}
	}
}

func TestExhaustedFailureFinalizesWorkflowDespiteRequestedRetry(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), testdatabase.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	var workflow string
	if err := pool.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&workflow); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM task_runs WHERE workflow_id=$1", workflow)
		_, _ = pool.Exec(ctx, "DELETE FROM workflow_runs WHERE workflow_id=$1", workflow)
	}()
	var task, callback int64
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(workflow+"task")))
	err = pool.QueryRow(ctx, `INSERT INTO task_runs(spec_version,spec_hash,queue_name,spec_json,status,attempts,max_attempts,leased_by,leased_until,workflow_id,lease_token)
		VALUES (1,$1,'budget-workflow','{"task_path":"tasks.test"}','RUNNING',2,2,'budget-worker',NOW()+INTERVAL '1 minute',$2,gen_random_uuid()) RETURNING result_id`, hash, workflow).Scan(&task)
	if err != nil {
		t.Fatal(err)
	}
	hash = fmt.Sprintf("%x", sha256.Sum256([]byte(workflow+"callback")))
	err = pool.QueryRow(ctx, `INSERT INTO task_runs(spec_version,spec_hash,queue_name,spec_json,status,wait_count,workflow_id)
		VALUES (1,$1,'budget-workflow','{"task_path":"tasks.test"}','WAITING',1,$2) RETURNING result_id`, hash, workflow).Scan(&callback)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow_runs(workflow_id,expected_count,callback_result_id,status) VALUES($1,1,$2,'RUNNING')`, workflow, callback); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestService(t, pool).CompleteFailure(ctx, task, "budget-worker", testLeaseToken(t, pool, task), json.RawMessage(`{"kind":"infra_error"}`), true, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	var taskStatus, workflowStatus, callbackStatus string
	var finished, cleared bool
	if err := pool.QueryRow(ctx, `SELECT status,finished_at IS NOT NULL,run_after IS NULL AND leased_by IS NULL AND leased_until IS NULL FROM task_runs WHERE result_id=$1`, task).Scan(&taskStatus, &finished, &cleared); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM workflow_runs WHERE workflow_id=$1", workflow).Scan(&workflowStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM task_runs WHERE result_id=$1", callback).Scan(&callbackStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "FAILED" || workflowStatus != "FAILED" || callbackStatus != "FAILED" || !finished || !cleared {
		t.Fatalf("inconsistent exhausted transition: task=%s workflow=%s callback=%s finished=%v lease/run_after cleared=%v", taskStatus, workflowStatus, callbackStatus, finished, cleared)
	}
}

func TestReplayPreservesTaskPathProjection(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), testdatabase.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	for _, byHash := range []bool{false, true} {
		name := fmt.Sprintf("replay-projection-%d-%v", time.Now().UnixNano(), byHash)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
		var source, replay int64
		err := pool.QueryRow(ctx, `INSERT INTO task_runs(spec_version,spec_hash,queue_name,spec_json,task_path,status) VALUES (1,$1,$2,'{"task_path":"tasks.test"}','tasks.test','FAILED') RETURNING result_id`, hash, name).Scan(&source)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(ctx, "DELETE FROM task_runs WHERE spec_hash=$1", hash)
		if byHash {
			_, replay, err = newTestService(t, pool).ReplayBySpecHash(ctx, hash)
		} else {
			replay, err = newTestService(t, pool).Replay(ctx, source)
		}
		if err != nil {
			t.Fatal(err)
		}
		var path *string
		if err := pool.QueryRow(ctx, "SELECT task_path FROM task_runs WHERE result_id=$1", replay).Scan(&path); err != nil {
			t.Fatal(err)
		}
		if path == nil || *path != "tasks.test" {
			t.Fatalf("replay lost task projection: %v", path)
		}
	}
}

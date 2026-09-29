package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type dispatchProcessResult struct {
	PID      int `json:"pid"`
	Admitted int `json:"admitted"`
	Blocked  int `json:"blocked"`
}

// A separately executed test process owns an independent sql.DB and node ID.
// Successful tickets remain UNKNOWN, simulating loss of the dispatching process.
func TestSchedulingDispatchProcessWorker(t *testing.T) {
	dsn := os.Getenv("SCHEDULING_PROCESS_DSN")
	if dsn == "" {
		t.Skip("parent-only subprocess entrypoint")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	store := NewPostgresStore(db)
	result := dispatchProcessResult{PID: os.Getpid()}
	for i := 0; i < 12; i++ {
		req := testDispatch(1, "")
		req.NodeID = fmt.Sprintf("process-%d", os.Getpid())
		req.HardConcurrency = 3
		ticket, err := store.BeginDispatch(context.Background(), req)
		if errors.Is(err, ErrCapacity) || errors.Is(err, ErrControlBlocked) {
			result.Blocked++
			continue
		}
		require.NoError(t, err)
		result.Admitted++
		require.NoError(t, store.MarkAttemptUnknown(context.Background(), ticket.TicketID))
	}
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	fmt.Println("PROCESS_RESULT=" + string(raw))
}

func runDispatchProcesses(t *testing.T, dsn string, count int) []dispatchProcessResult {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	outputs := make([][]byte, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSchedulingDispatchProcessWorker$", "-test.v")
			cmd.Env = append(os.Environ(), "SCHEDULING_PROCESS_DSN="+dsn)
			outputs[i], errs[i] = cmd.CombinedOutput()
		}(i)
	}
	wg.Wait()
	results := make([]dispatchProcessResult, count)
	for i := range results {
		require.NoError(t, errs[i], string(outputs[i]))
		found := false
		for _, line := range strings.Split(string(outputs[i]), "\n") {
			if strings.HasPrefix(line, "PROCESS_RESULT=") {
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "PROCESS_RESULT=")), &results[i]))
				found = true
			}
		}
		require.True(t, found, string(outputs[i]))
		require.NotEqual(t, os.Getpid(), results[i].PID)
		t.Logf("independent_process=%d admitted=%d blocked=%d", results[i].PID, results[i].Admitted, results[i].Blocked)
	}
	return results
}

func TestSchedulingCrossProcessAdmissionAndUnknownRetention(t *testing.T) {
	store, db := isolatedControlStore(t)
	var schema string
	require.NoError(t, db.QueryRow("SELECT current_schema()").Scan(&schema))
	u, err := url.Parse(os.Getenv("SCHEDULING_TEST_POSTGRES_DSN"))
	require.NoError(t, err)
	q := u.Query()
	q.Set("options", "-csearch_path="+schema)
	u.RawQuery = q.Encode()
	results := runDispatchProcesses(t, u.String(), 2)
	require.NotEqual(t, results[0].PID, results[1].PID)
	require.Equal(t, 3, results[0].Admitted+results[1].Admitted)
	var active int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE state<>'settled'").Scan(&active))
	require.Equal(t, 3, active)
	_, err = db.Exec("UPDATE scheduling_attempts SET lease_until=NOW()-INTERVAL '1 minute'")
	require.NoError(t, err)
	_, err = store.ReconcileExpired(context.Background())
	require.NoError(t, err)
	results = runDispatchProcesses(t, u.String(), 2)
	require.Zero(t, results[0].Admitted+results[1].Admitted, "node restart or TTL must not release unknown work")
	_, err = store.Control(context.Background(), ControlCommand{AccountID: 1, Action: "pause"})
	require.NoError(t, err)
	results = runDispatchProcesses(t, u.String(), 1)
	require.Zero(t, results[0].Admitted)
	_, err = store.Control(context.Background(), ControlCommand{AccountID: 1, Action: "resume"})
	require.NoError(t, err)
	results = runDispatchProcesses(t, u.String(), 1)
	require.Zero(t, results[0].Admitted, "resume changes admission, not remote certainty")
	var ticket string
	require.NoError(t, db.QueryRow("SELECT ticket_id FROM scheduling_attempts ORDER BY ticket_id LIMIT 1").Scan(&ticket))
	require.NoError(t, store.SettleAttempt(context.Background(), ticket, "test_remote_terminal", false))
	results = runDispatchProcesses(t, u.String(), 2)
	require.Equal(t, 1, results[0].Admitted+results[1].Admitted)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE state<>'settled'").Scan(&active))
	require.Equal(t, 3, active)
}

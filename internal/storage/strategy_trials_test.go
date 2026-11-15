package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

func registryStart(id string) strategytrial.Start {
	return strategytrial.Start{ID: id, Family: "trend", StrategyID: "strategy-" + strings.Repeat("a", 64), Spec: json.RawMessage(`{"name":"trend","version":"1","params":{"lookback":"20"}}`), Config: json.RawMessage(`{"start":"2016-01-01","cash":"2000","factor":1e0}`), DataSHA256: strings.Repeat("b", 64), EvidenceClass: "synthetic_fixture", Parents: []string{}, StartedAt: time.Date(2026, 11, 14, 23, 0, 0, 123456000, time.UTC)}
}

func TestMemoryStrategyTrials(t *testing.T) { strategyTrialContracts(t, NewMemory()) }
func TestPostgresContractsStrategyTrials(t *testing.T) {
	p := timerPostgres(t, "strategy_trials")
	strategyTrialContracts(t, p)
	ctx := context.Background()
	// Upgrade tests can replay later migrations over existing tables. Keep
	// the retained trials and re-install their guards without changing data.
	if _, err := p.pool.Exec(ctx, migrations.StrategyTrials); err != nil {
		t.Fatal("migration replay", err)
	}
	for _, table := range []string{"mantik_strategy_trials", "mantik_strategy_trial_returns", "mantik_strategy_trial_outcomes"} {
		for _, sql := range []string{"UPDATE " + table + " SET trial_id=trial_id", "DELETE FROM " + table, "TRUNCATE " + table + " CASCADE"} {
			if table == "mantik_strategy_trials" && strings.HasPrefix(sql, "UPDATE") {
				sql = "UPDATE " + table + " SET id=id"
			}
			if _, err := p.pool.Exec(ctx, sql); err == nil {
				t.Fatalf("history changed: %s", sql)
			}
		}
	}
	// SQL callers must obey lifecycle ordering too, not just the Go methods.
	for _, sql := range []string{
		`INSERT INTO mantik_strategy_trial_returns VALUES('complete','2026-01-07','0','2000')`,
		`INSERT INTO mantik_strategy_trial_returns VALUES('unfinished','2026-01-04','0','2000')`,
		`INSERT INTO mantik_strategy_trial_outcomes VALUES('unfinished','crashed','2020-01-01')`,
	} {
		if _, err := p.pool.Exec(ctx, sql); err == nil {
			t.Fatal("invalid SQL append admitted", sql)
		}
	}
	// Reopening the store must retain unfinished work and the search total.
	reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if n, err := reopened.StrategyTrialSearchTotal(ctx, "trend"); err != nil || n != 6 {
		t.Fatal(n, err)
	}
	r, err := reopened.StrategyTrial(ctx, "unfinished")
	if err != nil || r.Outcome != nil || len(r.Returns) != 1 {
		t.Fatal(r, err)
	}
}

func strategyTrialContracts(t *testing.T, s strategytrial.Store) {
	t.Helper()
	ctx := context.Background()
	if n, err := s.StrategyTrialSearchTotal(ctx, "trend"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	for _, id := range []string{"complete", "failed", "crashed", "abandoned", "unfinished"} {
		start := registryStart(id)
		if created, err := s.StartStrategyTrial(ctx, start); err != nil || !created {
			t.Fatal(created, err)
		}
		if created, err := s.StartStrategyTrial(ctx, start); err != nil || created {
			t.Fatal("retry counted twice", created, err)
		}
		start.Family = "rotation"
		if _, err := s.StartStrategyTrial(ctx, start); !errors.Is(err, strategytrial.ErrConflict) {
			t.Fatal("changed start accepted", err)
		}
		day := strategytrial.DailyReturn{Date: "2026-01-05", Return: "-0.01", Equity: "1980"}
		if err := s.AppendStrategyTrialReturn(ctx, id, day); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendStrategyTrialReturn(ctx, id, day); err != nil {
			t.Fatal("retry failed", err)
		}
		day.Return = "0.5"
		if err := s.AppendStrategyTrialReturn(ctx, id, day); !errors.Is(err, strategytrial.ErrConflict) {
			t.Fatal("return rewritten", err)
		}
		day.Date = "2026-01-04"
		if err := s.AppendStrategyTrialReturn(ctx, id, day); !errors.Is(err, strategytrial.ErrConflict) {
			t.Fatal("backdated return", err)
		}
		if id == "unfinished" {
			continue
		}
		status := id
		if id == "complete" {
			status = "completed"
		}
		o := strategytrial.Outcome{Status: status, FinishedAt: start.StartedAt.Add(time.Minute)}
		if err := s.FinishStrategyTrial(ctx, id, o); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishStrategyTrial(ctx, id, o); err != nil {
			t.Fatal(err)
		}
		o.Status = "completed"
		o.FinishedAt = o.FinishedAt.Add(time.Second)
		if err := s.FinishStrategyTrial(ctx, id, o); !errors.Is(err, strategytrial.ErrConflict) {
			t.Fatal("outcome rewritten", err)
		}
		day.Date = "2026-01-06"
		if err := s.AppendStrategyTrialReturn(ctx, id, day); !errors.Is(err, strategytrial.ErrConflict) {
			t.Fatal("return after completion", err)
		}
	}
	if n, err := s.StrategyTrialSearchTotal(ctx, "trend"); err != nil || n != 5 {
		t.Fatal("failed attempts missing from search total", n, err)
	}
	if n, err := s.StrategyTrialSearchTotal(ctx, "rotation"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	// Lineage binds earlier attempts, not merely a caller-supplied strategy name.
	child := registryStart("child")
	child.Parents = []string{"failed", "complete"}
	if _, err := s.StartStrategyTrial(ctx, child); err != nil {
		t.Fatal(err)
	}
	child.Parents = []string{"complete", "failed"}
	if created, err := s.StartStrategyTrial(ctx, child); err != nil || created {
		t.Fatal(created, err)
	}
	child.ID = "bad-parent"
	child.Parents = []string{"missing"}
	if _, err := s.StartStrategyTrial(ctx, child); !errors.Is(err, strategytrial.ErrNotFound) {
		t.Fatal(err)
	}
	child.Parents = []string{"child"}
	child.StartedAt = child.StartedAt.Add(-time.Second)
	if _, err := s.StartStrategyTrial(ctx, child); !errors.Is(err, strategytrial.ErrConflict) {
		t.Fatal(err)
	}
	// Read values cannot mutate retained data.
	before, err := s.StrategyTrial(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	altered := copyStrategyTrial(before)
	altered.Start.Spec[0] = 'x'
	altered.Start.Config[0] = 'x'
	altered.Start.Parents[0] = "missing"
	after, err := s.StrategyTrial(ctx, "child")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("retained start changed", err)
	}
	complete, err := s.StrategyTrial(ctx, "complete")
	if err != nil {
		t.Fatal(err)
	}
	complete.Returns[0].Return = "99"
	complete.Outcome.Status = "crashed"
	complete, err = s.StrategyTrial(ctx, "complete")
	if err != nil || complete.Returns[0].Return != "-0.01" || complete.Outcome.Status != "completed" {
		t.Fatal(complete, err)
	}
	if _, err := s.StrategyTrial(ctx, "missing"); !errors.Is(err, strategytrial.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.AppendStrategyTrialReturn(ctx, "missing", strategytrial.DailyReturn{Date: "2026-01-01", Return: "0", Equity: "1"}); !errors.Is(err, strategytrial.ErrNotFound) {
		t.Fatal(err)
	}
	invalid := registryStart("invalid")
	invalid.EvidenceClass = "qualified"
	if _, err := s.StartStrategyTrial(ctx, invalid); !errors.Is(err, strategytrial.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.AppendStrategyTrialReturn(ctx, "unfinished", strategytrial.DailyReturn{Date: "2026-02-30", Return: "NaN", Equity: "0"}); !errors.Is(err, strategytrial.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.FinishStrategyTrial(ctx, "unfinished", strategytrial.Outcome{Status: "running", FinishedAt: invalid.StartedAt}); !errors.Is(err, strategytrial.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.FinishStrategyTrial(ctx, "unfinished", strategytrial.Outcome{Status: "completed", FinishedAt: invalid.StartedAt.Add(-time.Second)}); !errors.Is(err, strategytrial.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.StrategyTrialSearchTotal(ctx, ""); !errors.Is(err, strategytrial.ErrInvalid) {
		t.Fatal(err)
	}
	// Concurrent exact starts count once; competing completions cannot rewrite.
	const workers = 8
	var wg sync.WaitGroup
	added := make([]bool, workers)
	errs := make([]error, workers)
	concurrent := registryStart("concurrent")
	concurrent.Family = "other"
	for i := range workers {
		wg.Go(func() { added[i], errs[i] = s.StartStrategyTrial(ctx, concurrent) })
	}
	wg.Wait()
	count := 0
	for i := range workers {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if added[i] {
			count++
		}
	}
	if count != 1 {
		t.Fatal(count)
	}
	for i := range workers {
		wg.Go(func() {
			errs[i] = s.FinishStrategyTrial(ctx, "concurrent", strategytrial.Outcome{Status: "completed", FinishedAt: concurrent.StartedAt.Add(time.Duration(i) * time.Second)})
		})
	}
	wg.Wait()
	count = 0
	for _, err := range errs {
		if err == nil {
			count++
		} else if !errors.Is(err, strategytrial.ErrConflict) {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatal("competing outcomes", count)
	}
	if n, err := s.StrategyTrialSearchTotal(ctx, "trend"); err != nil || n != 6 {
		t.Fatal(n, err)
	}
}

// The child exits without cleanup or a terminal write, as an interrupted
// worker would. Its committed start and partial return must survive.
func TestPostgresContractsStrategyTrialProcessExit(t *testing.T) {
	ctx := context.Background()
	if database := os.Getenv("MANTIK_STRATEGY_TRIAL_CRASH_URL"); database != "" {
		p, err := OpenPostgres(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.StartStrategyTrial(ctx, registryStart("process-exit")); err != nil {
			t.Fatal(err)
		}
		if err = p.AppendStrategyTrialReturn(ctx, "process-exit", strategytrial.DailyReturn{Date: "2026-01-05", Return: "0.02", Equity: "2040"}); err != nil {
			t.Fatal(err)
		}
		os.Exit(23)
	}
	p := timerPostgres(t, "strategy_trial_exit")
	child := exec.Command(os.Args[0], "-test.run=^TestPostgresContractsStrategyTrialProcessExit$")
	child.Env = append(os.Environ(), "MANTIK_STRATEGY_TRIAL_CRASH_URL="+p.pool.Config().ConnString())
	err := child.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatal("child did not reach the interruption", err)
	}
	r, err := p.StrategyTrial(ctx, "process-exit")
	if err != nil || r.Outcome != nil || len(r.Returns) != 1 {
		t.Fatal(r, err)
	}
	if n, err := p.StrategyTrialSearchTotal(ctx, "trend"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = p.FinishStrategyTrial(ctx, "process-exit", strategytrial.Outcome{Status: "abandoned", FinishedAt: r.Start.StartedAt.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r, err = p.StrategyTrial(ctx, "process-exit")
	if err != nil || r.Outcome.Status != "abandoned" || len(r.Returns) != 1 {
		t.Fatal(r, err)
	}
}

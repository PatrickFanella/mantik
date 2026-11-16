package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"github.com/jackc/pgx/v5"
)

func TestForwardCLIExplicitBoundary(t *testing.T) {
	var out, errOut bytes.Buffer
	if e := Run(context.Background(), []string{"forward-declaration", "--help"}, &out, &errOut); e != nil || !strings.Contains(errOut.String(), "database-env") {
		t.Fatal(e, errOut.String())
	}
	t.Setenv("MANTIK_EMPTY_FORWARD_DB", "")
	if e := Run(context.Background(), []string{"forward-declaration", "--id", "test", "--database-env", "MANTIK_EMPTY_FORWARD_DB"}, &out, &errOut); e == nil {
		t.Fatal("implicit DB accepted")
	}
}
func TestPostgresContractsForwardCLI(t *testing.T) {
	base := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("use make test-integration")
	}
	ctx := context.Background()
	conn, e := pgx.Connect(ctx, base)
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(base)
	if e != nil {
		t.Fatal(e)
	}
	db := "mantik_test_forward_cli_" + strings.ToLower(rand.Text())
	if _, e = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()+" TEMPLATE template0"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
		_ = conn.Close(ctx)
	})
	u.Path = "/" + db
	s, e := storage.OpenPostgres(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	spec := strategy.Spec{Name: "model", Version: "cli-v1", ModelDriven: true, Data: []string{strategy.KindDailyBars}}
	sid, e := spec.ID()
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(spec)
	if e != nil {
		t.Fatal(e)
	}
	g := strategygate.Declaration{ID: "cli-gate", Family: "cli", Account: "cash", StrategyID: sid, Spec: raw, Policy: "backtest-gate-v1", Benchmark: "cash", DataSHA256: strings.Repeat("a", 64)}
	gate, e := s.DeclareStrategyGate(ctx, g)
	if e != nil {
		t.Fatal(e)
	}
	hash, e := strategygate.Hash(g)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.RecordStrategyGate(ctx, strategygate.Decision{ID: g.ID, StrategyID: sid, DeclarationSHA256: hash, FamilySHA256: strings.Repeat("b", 64), Policy: g.Policy, EvidenceClass: "forward_only", Status: "forward_only", Stage: "incubation", Minimum: "1", At: gate.DeclaredAt, Checks: []strategygate.Check{{Code: "contract-fixture", Reason: "no qualification", Skipped: true}}})
	if e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC()
	_, e = s.CreateCappedBook(ctx, account.DefaultCappedConfig("cli-book", "cli-paper", account.Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	d := forward.Declaration{ID: "cli-forward", Family: g.Family, GateID: g.ID, BookID: "cli-book", SleeveID: "cli-sleeve", StrategyID: sid, Spec: spec, Benchmark: forward.Benchmark{StrategyID: sid, Spec: spec}, WindowStart: at.Add(time.Hour), WindowEnd: at.Add(28 * 24 * time.Hour), OutcomeDeadline: at.Add(56 * 24 * time.Hour), MaxHoldingSeconds: 28 * 86400, EligibilityTTLSeconds: 28 * 86400, MinSessions: 20, MinIndependentDecisions: 10, MinimumNetExcess: "0.01", MaximumDrawdown: "0.15", MaxSearchTrials: 8}
	raw, e = json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "declaration.json")
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("MANTIK_FORWARD_CLI_DB", u.String())
	var first string
	for _, action := range []string{"declare", "declare", "show", "eligibility"} {
		var out, errOut bytes.Buffer
		if e = Run(ctx, []string{"forward-declaration", "--database-env", "MANTIK_FORWARD_CLI_DB", "--id", d.ID, "--action", action, "--input", file}, &out, &errOut); e != nil {
			t.Fatal(action, e, errOut.String())
		}
		if action == "eligibility" {
			var v forward.Eligibility
			if e = json.Unmarshal(out.Bytes(), &v); e != nil || v.EntryEligible || v.QualificationCredit {
				t.Fatal(v, e)
			}
			continue
		}
		var r forward.Record
		if e = json.Unmarshal(out.Bytes(), &r); e != nil || r.Declaration.ID != d.ID || r.QualificationCredit {
			t.Fatal(r, e)
		}
		if first == "" {
			first = r.SHA256
		}
		if r.SHA256 != first {
			t.Fatal("CLI retry/reopen changed identity")
		}
	}
}

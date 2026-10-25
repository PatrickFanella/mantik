package storage

import (
	"context"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresContractsPaperLedgerRetryCommit(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "ledgerretrycommit")
	namespace, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0).UTC()
	account, err := s.CreatePaperAccount(ctx, namespace.ID, paperaccount.Config{ID: "retry-account", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 60000}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	genesis, err := s.PaperLedgerRehearsal(ctx, account.Account.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	command := paperledger.AppendCommand{ReceiverID: namespace.ID, AccountSHA256: account.Account.SHA256, ExpectedHeadSHA256: genesis.Result.LedgerHeadSHA256, ScopeCode: paperledger.RehearsalScope, Entry: paperledger.Entry{ID: "retained-reservation", Kind: "reserve", ReservationID: "fixture-position", AmountCents: 40000, OccurredAt: at}}
	original, err := s.AppendPaperLedgerRehearsal(ctx, account.Account.Config.ID, command, func() time.Time { return at })
	if err != nil || original.Duplicate {
		t.Fatal(original, err)
	}
	before, err := s.PaperLedgerRehearsal(ctx, account.Account.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := s.PaperAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	noClock := func() time.Time { panic("exact ledger retry sampled a clock") }
	for _, op := range []struct {
		name string
		call func(context.Context, *Postgres) (any, error)
	}{
		{"read", func(ctx context.Context, p *Postgres) (any, error) {
			return p.PaperLedgerRehearsal(ctx, account.Account.Config.ID)
		}},
		{"duplicate_retry", func(ctx context.Context, p *Postgres) (any, error) {
			return p.AppendPaperLedgerRehearsal(ctx, account.Account.Config.ID, command, noClock)
		}},
	} {
		t.Run(op.name, func(t *testing.T) {
			request, cancel := context.WithCancel(ctx)
			defer cancel()
			tracer := &cancelCollectorCommit{cancel: cancel}
			config := s.pool.Config()
			config.ConnConfig.Tracer = tracer
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			got, err := op.call(request, &Postgres{pool: pool})
			pool.Close()
			if !tracer.fired.Load() || err == nil {
				t.Fatal("COMMIT cancellation did not fire", got, err, tracer.fired.Load())
			}
			if !reflect.ValueOf(got).IsZero() {
				t.Errorf("failed COMMIT exposed populated ledger acknowledgement: %#v", got)
			}
			actual, err := s.PaperLedgerRehearsal(ctx, account.Account.Config.ID)
			if err != nil || !reflect.DeepEqual(actual, before) {
				t.Fatal("failed COMMIT changed ledger", err)
			}
		})
	}
	retry, err := s.AppendPaperLedgerRehearsal(ctx, account.Account.Config.ID, command, noClock)
	expected := original
	expected.Duplicate = true
	if err != nil || !reflect.DeepEqual(retry, expected) || retry.Validate(account.Account.Config.ID, command) != nil {
		t.Fatal("successful clock-free retry changed", retry, err)
	}
	reopened, err := OpenPostgres(ctx, s.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	actual, err := reopened.PaperLedgerRehearsal(ctx, account.Account.Config.ID)
	reopened.Close()
	if err != nil || !reflect.DeepEqual(actual, before) {
		t.Fatal("reopen changed original durable ledger", err)
	}
	after, err := s.PaperAccounts(ctx)
	if err != nil || !reflect.DeepEqual(after, accounts) {
		t.Fatal("retry changed account", err)
	}
	opsAfter, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(opsAfter, operations) {
		t.Fatal("retry changed operational state", err)
	}
}

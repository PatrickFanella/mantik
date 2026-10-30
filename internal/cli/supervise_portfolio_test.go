package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func bindPortfolioMemory(t *testing.T, m *storage.Memory) paperaccount.Account {
	t.Helper()
	ctx := context.Background()
	receiver, err := m.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: "portfolio-paper", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 100000}, func() time.Time { return workflow.Fixtures()[0].ReceivedAt })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BindFixtureCapital(ctx, storage.FixtureCapitalBinding{ReceiverID: receiver.ID, AccountID: r.Account.Config.ID, AccountSHA256: r.Account.SHA256, ScopeCode: storage.FixtureCapitalBindingScope}); err != nil {
		t.Fatal(err)
	}
	return r.Account
}
func portfolioFixture(t *testing.T) (*storage.Memory, portfolioReadback) {
	t.Helper()
	ctx := context.Background()
	m := storage.NewQueuedMemory()
	a := bindPortfolioMemory(t, m)
	for _, i := range []int{1, 3} {
		q := workflow.Fixtures()[i]
		if _, err := m.Ingest(ctx, q); err != nil {
			t.Fatal(err)
		}
		if err := service.RunActionCycle(ctx, m, "portfolio-worker", rehearsalClock{q.ReceivedAt}, time.Second*30); err != nil {
			t.Fatal(err)
		}
	}
	v, err := m.FixtureCapital(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return m, portfolioReadback{ReceiverID: a.ReceiverID, Account: a, Capital: v}
}

func TestPortfolioSupervisionHTTPJourney(t *testing.T) {
	ctx := context.Background()
	backend, p := portfolioFixture(t)
	before, _ := backend.Snapshot(ctx)
	accounts, _ := backend.PaperAccounts(ctx)
	var mu sync.Mutex
	var requests []string
	handler := service.Handler(backend, "portfolio-token")
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		handler.ServeHTTP(w, r)
	})))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "portfolio-token")
	first, second := p.Capital.Capital.Commitments[0].ActionID, p.Capital.Capital.Commitments[1].ActionID
	commands := "d " + second + "\nd " + first + "\nn\nd " + second + "\nn\np\nexecute\nr\nq\n"
	var out bytes.Buffer
	if err := supervisePortfolio(ctx, io.NopCloser(strings.NewReader(commands)), &out, 1, func(ctx context.Context) (portfolioReadback, error) { return fetchPortfolio(ctx, server.URL) }); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /v1/fixture-capital", "GET /v1/paper-accounts/portfolio-paper", "GET /v1/fixture-capital", "GET /v1/paper-accounts/portfolio-paper"}
	mu.Lock()
	got := append([]string{}, requests...)
	mu.Unlock()
	if !reflect.DeepEqual(want, got) {
		t.Fatal("navigation/details caused requests or mutation", got)
	}
	for _, text := range []string{"READBACK SYNTHETIC FIXTURE PORTFOLIO", "available=94000 reserved=0 position_at_cost=0 realized_fixture_pnl=-6000", "Position: none", "unqualified", "COMMITMENTS 1..1 OF 2", "COMMITMENTS 2..2 OF 2", "End of commitments", "no request sent", first, second} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("missing portfolio state", text, out.String())
		}
	}
	if strings.Count(out.String(), "DISPLAYED FIXTURE COMMITMENT") != 2 {
		t.Fatal("off-page detail allowed", out.String())
	}
	after, _ := backend.Snapshot(ctx)
	afterAccounts, _ := backend.PaperAccounts(ctx)
	afterCapital, _ := backend.FixtureCapital(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, afterAccounts) || !reflect.DeepEqual(p.Capital, afterCapital) {
		t.Fatal("viewer mutated source")
	}
}

func TestPortfolioUnboundThenExplicitActivation(t *testing.T) {
	ctx := context.Background()
	backend := storage.NewQueuedMemory()
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(backend, "portfolio-token")))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "portfolio-token")
	reads := 0
	var out bytes.Buffer
	err := supervisePortfolio(ctx, io.NopCloser(strings.NewReader("n\nd any\nr\nq\n")), &out, 1, func(ctx context.Context) (portfolioReadback, error) {
		reads++
		if reads == 2 {
			bindPortfolioMemory(t, backend)
		} // Explicit activation elsewhere.
		return fetchPortfolio(ctx, server.URL)
	})
	if err != nil || reads != 2 || !strings.Contains(out.String(), "UNBOUND: no fixture capital") || !strings.Contains(out.String(), "available=100000") {
		t.Fatal(err, out.String())
	}
}

func TestPortfolioFailedRefreshRetainsDetachedStalePage(t *testing.T) {
	_, good := portfolioFixture(t)
	raw, _ := json.Marshal(good)
	shared := good
	calls := 0
	var out bytes.Buffer
	id := good.Capital.Capital.Commitments[0].ActionID
	err := supervisePortfolio(context.Background(), io.NopCloser(strings.NewReader("r\nn\nd "+id+"\nr\nd "+id+"\nq\n")), &out, 1, func(context.Context) (portfolioReadback, error) {
		calls++
		if calls == 2 {
			shared.Capital.Capital.Commitments[0].Code = "unvalidated-alias"
			return portfolioReadback{}, errors.New("offline")
		}
		if calls == 3 {
			var p portfolioReadback
			_ = json.Unmarshal(raw, &p)
			return p, nil
		}
		return shared, nil
	})
	if err != nil || calls != 3 || !strings.Contains(out.String(), "STALE SYNTHETIC FIXTURE PORTFOLIO") || !strings.Contains(out.String(), "STALE: portfolio navigation and details") || strings.Contains(out.String(), "unvalidated-alias") || strings.Count(out.String(), "DISPLAYED FIXTURE COMMITMENT") != 1 {
		t.Fatal(err, out.String())
	}
}

func TestPortfolioInvalidReadbackBlocksDisplayAndDetails(t *testing.T) {
	_, good := portfolioFixture(t)
	for name, mutate := range map[string]func(*portfolioReadback){
		"hash":          func(p *portfolioReadback) { p.Capital.Capital.SHA256 = "changed" },
		"funding":       func(p *portfolioReadback) { p.Account.Config.CapitalLimitCents-- },
		"qualification": func(p *portfolioReadback) { p.Capital.QualificationCredit = true },
		"receiver":      func(p *portfolioReadback) { p.ReceiverID = "receiver-other_other_other" },
		"false_unbound": func(p *portfolioReadback) { p.Unbound = true },
	} {
		t.Run(name, func(t *testing.T) {
			bad := good
			mutate(&bad)
			calls := 0
			var out bytes.Buffer
			err := supervisePortfolio(context.Background(), io.NopCloser(strings.NewReader("n\nd any\nr\nq\n")), &out, 1, func(context.Context) (portfolioReadback, error) { calls++; return bad, nil })
			if err != nil || calls != 2 || strings.Contains(out.String(), "READBACK OK") || strings.Contains(out.String(), "USD CENTS") || strings.Contains(out.String(), "DISPLAYED FIXTURE COMMITMENT") {
				t.Fatal(err, out.String())
			}
		})
	}
}

func TestPortfolioPinsOwnerAccountAndDurabilityUntilReconnect(t *testing.T) {
	_, good := portfolioFixture(t)
	_, other := portfolioFixture(t)
	changedAccount := func(id string, cash int64) portfolioReadback {
		a, err := paperaccount.Create(good.ReceiverID, paperaccount.Config{ID: id, Currency: "USD", OpeningCashCents: cash, CapitalLimitCents: cash}, good.Account.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		b, err := fixturecapital.New(a)
		if err != nil {
			t.Fatal(err)
		}
		s, _ := b.Snapshot()
		return portfolioReadback{ReceiverID: a.ReceiverID, Account: a, Capital: storage.FixtureCapitalReadback{Schema: storage.FixtureCapitalReadbackSchema, Authority: storage.FixtureCapitalReadbackAuthority, WorkerBound: true, Capital: s}}
	}
	changedDurability := good
	changedDurability.Capital.Durable = true
	for name, changed := range map[string]portfolioReadback{
		"receiver": other, "account": changedAccount("replacement", 100000), "funding": changedAccount(good.Account.Config.ID, 99999), "durability": changedDurability, "binding_removed": {ReceiverID: good.ReceiverID, Unbound: true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := changed.validate(); err != nil {
				t.Fatal("invalid pin-change fixture", err)
			}
			calls := 0
			var out bytes.Buffer
			err := supervisePortfolio(context.Background(), io.NopCloser(strings.NewReader("r\nd "+good.Capital.Capital.Commitments[0].ActionID+"\nr\nq\n")), &out, 1, func(context.Context) (portfolioReadback, error) {
				calls++
				if calls == 2 {
					return changed, nil
				}
				return good, nil
			})
			if err != nil || calls != 3 || strings.Count(out.String(), "READBACK OK") != 2 || !strings.Contains(out.String(), "identity changed") || !strings.Contains(out.String(), "STALE SYNTHETIC FIXTURE PORTFOLIO") || strings.Contains(out.String(), "DISPLAYED FIXTURE COMMITMENT") || strings.Contains(out.String(), "UNBOUND: no fixture capital account is active") {
				t.Fatal(err, out.String())
			}
		})
	}
}

func TestPortfolioUnknownEndpointIsNotUnbound(t *testing.T) {
	for _, code := range []int{404, 401, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(code)
				if code != 404 {
					_, _ = io.WriteString(w, `{"code":"fixture_capital_unbound"}`)
				}
			})))
			defer server.Close()
			p, err := fetchPortfolio(context.Background(), server.URL)
			if err == nil || p.Unbound || calls.Load() != 1 {
				t.Fatal("unavailable endpoint treated as unbound", p, err, calls.Load())
			}
		})
	}
}

func TestPortfolioCancellationClosesOwnedInput(t *testing.T) {
	_, good := portfolioFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, w := io.Pipe()
	defer w.Close()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- supervisePortfolio(ctx, in, io.Discard, 10, func(context.Context) (portfolioReadback, error) { close(ready); return good, nil })
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("portfolio viewer left blocked input")
	}
}

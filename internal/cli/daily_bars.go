package cli

import (
	"context"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
)

type dailyBarFlags struct{ universe, credentials, journal, start string }
type dailyBarSource struct {
	journal *alpaca.Journal
	client  *alpaca.Client
}

func (s dailyBarSource) Calendar(ctx context.Context, start, end string) ([]alpaca.Session, error) {
	return s.journal.DailyCalendar(ctx, s.client, start, end)
}
func (s dailyBarSource) Bars(ctx context.Context, symbol string, start, end time.Time) (alpaca.DailyBars, error) {
	return s.journal.CollectDailyBars(ctx, s.client, []string{symbol}, start, end)
}

func openDailyBars(f dailyBarFlags) (*service.DailyBarWorker, *alpaca.Journal, error) {
	if f == (dailyBarFlags{}) {
		return nil, nil, nil
	}
	if f.universe == "" || f.credentials == "" || f.journal == "" || f.start == "" {
		return nil, nil, errors.New("daily bars require universe, credentials-file, journal and start together")
	}
	start, err := time.Parse(time.DateOnly, f.start)
	if err != nil {
		return nil, nil, errors.New("daily-bars-start must be YYYY-MM-DD")
	}
	var universe marketdata.UniverseReport
	if err := readPaperJSON(f.universe, &universe, false); err != nil {
		return nil, nil, err
	}
	if universe.AsOf.IsZero() || universe.AsOf.After(time.Now()) {
		return nil, nil, errors.New("daily bars require a dated stock-universe report")
	}
	assets := []marketdata.BackfillAsset{}
	for _, a := range universe.Members {
		assets = append(assets, marketdata.BackfillAsset{Symbol: a.Symbol, AssetID: a.AssetID})
	}
	var creds alpaca.PaperCredentials
	if err := readPaperJSON(f.credentials, &creds, true); err != nil {
		return nil, nil, err
	}
	client, err := alpaca.NewReadClient(creds, "sip")
	if err != nil {
		return nil, nil, err
	}
	worker, err := service.NewDailyBarWorker(assets, start, dailyBarSource{client: client})
	if err != nil {
		return nil, nil, err
	}
	journal, err := alpaca.OpenJournal(f.journal)
	if err != nil {
		return nil, nil, err
	}
	worker, err = service.NewDailyBarWorker(assets, start, dailyBarSource{client: client, journal: journal})
	if err != nil {
		_ = journal.Close()
		return nil, nil, err
	}
	return worker, journal, nil
}

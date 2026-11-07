package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// SelectPaperExecutionQuotes is the store's execution quote source. It returns
// one command per retained quote of one registration that became available in
// (After, Through], in availability order. A quote counts only with documented
// share sizes, the strategy's symbol and evidence class, a fill-method range,
// an age within the strategy limit at availability and open regular-session
// evidence at that clock: the same filter the evaluation's protective replay
// applies. Captures are validated against owner custody as the outcome market
// inspector validates them.
func SelectPaperExecutionQuotes(ctx context.Context, feed storage.FeedEvidenceSnapshot, q storage.PaperExecutionQuoteQuery) ([]paperfill.BookCommand, error) {
	entry, ok := feed.Collectors.Entries[q.RegistrationID]
	if !ok || entry.Registration.Provider != "alpaca" || entry.Registration.Feed != q.Feed || feed.Routing == nil || q.Through.Before(q.After) || q.MaxQuoteAgeSeconds < 1 {
		return nil, storage.ErrOwnedAllocationInvalid
	}
	ctx = withSessionMemo(ctx)
	prefixes := newFeedPrefixes(feed, q.RegistrationID)
	memo := sessionMemoFrom(ctx)
	rows := []PaperOutcomeMarketRow{}
	seen := map[string]bool{}
	for key, published := range feed.Records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if published.RegistrationID != q.RegistrationID || published.PublishedAt.After(q.Through) {
			continue
		}
		var recording alpaca.Recording
		if json.Unmarshal(published.Recording, &recording) != nil || key != published.RegistrationID+":"+published.Key || published.Key != recordingKey(recording) || published.Fence.RegistrationID != q.RegistrationID || feedcollector.ValidateFence(published.Fence) != nil || published.PublishedAt.IsZero() || published.PublishedAt.Before(recording.ReceivedAt) {
			return nil, storage.ErrOwnedAllocationConflict
		}
		if err := validateFeedRecording(feed, q.RegistrationID, recording); err != nil {
			return nil, err
		}
		if recording.Capability != "latest_quotes" {
			continue
		}
		normalized, err := alpaca.Normalize(recording)
		if err != nil {
			return nil, err
		}
		for _, input := range normalized.Inputs {
			admission, ok := feed.Collectors.Admissions[input.ID]
			if !ok || admission.Fence.RegistrationID != q.RegistrationID || feedcollector.ValidateFence(admission.Fence) != nil || admission.InputSHA256 != feedcollector.InputDigest(input) || admission.At.Before(input.ReceivedAt) {
				return nil, storage.ErrOwnedAllocationConflict
			}
			if seen[input.ID] {
				return nil, storage.ErrOwnedAllocationConflict
			}
			seen[input.ID] = true
			available := published.PublishedAt
			if admission.At.After(available) {
				available = admission.At
			}
			if input.Kind != "quote" || input.Provenance == nil || input.Provenance.Quality != "usable" || !available.After(q.After) || available.After(q.Through) {
				continue
			}
			dimensions, err := alpaca.InspectQuoteShareDimensions(recording, input.ID)
			if err != nil {
				return nil, err
			}
			row := PaperOutcomeMarketRow{InputSHA256: feedcollector.InputDigest(input), InputKind: input.Kind, InputID: input.ID, CaptureKey: key, CaptureSHA256: recording.SHA256, EvidenceClass: input.EvidenceClass, Quality: input.Provenance.Quality, QualityReason: input.Provenance.Reason, OccurredAt: input.OccurredAt, ReceivedAt: input.ReceivedAt, PublishedAt: published.PublishedAt, AdmittedAt: admission.At, AvailableAt: available, TemporalStatus: "execution_window", DimensionStatus: "dimensions_retained_share_units_unverified", Quote: &dimensions, PublicationFence: published.Fence, AdmissionFence: admission.Fence}
			if dimensions.ShareQuantitiesAvailable {
				row.DimensionStatus = "dimensions_retained_documented_share_units"
			}
			if !executionQuoteEligible(row, q) {
				continue
			}
			session, err := memo.inspect(ctx, prefixes, feed, q.RegistrationID, row)
			if err != nil {
				return nil, err
			}
			if !session.Evidence.Quote.RegularSessionEligible || session.Evidence.Session.Status != "open" {
				continue
			}
			row.SessionEvidence = &session
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b PaperOutcomeMarketRow) int {
		if c := a.AvailableAt.Compare(b.AvailableAt); c != 0 {
			return c
		}
		if c := a.OccurredAt.Compare(b.OccurredAt); c != 0 {
			return c
		}
		return strings.Compare(a.InputID, b.InputID)
	})
	commands := make([]paperfill.BookCommand, 0, len(rows))
	for _, r := range rows {
		d := r.Quote
		quote := paperfill.Quote{ID: "quote-" + workerplan.Hash(r.InputID), SourceSHA256: workerplan.Hash(r), Symbol: d.Symbol, Currency: d.Currency, Quality: "usable", EvidenceClass: r.EvidenceClass, OccurredAt: d.OccurredAt, ReceivedAt: r.AvailableAt, BidCents: d.BidCents, AskCents: d.AskCents, BidQuantity: d.BidShares, AskQuantity: d.AskShares}
		commands = append(commands, paperfill.BookCommand{ID: quote.ID, At: r.AvailableAt, Quote: &quote})
	}
	return commands, ctx.Err()
}

func executionQuoteEligible(r PaperOutcomeMarketRow, q storage.PaperExecutionQuoteQuery) bool {
	d := r.Quote
	return d != nil && d.ShareQuantitiesAvailable && d.Symbol == q.Symbol && r.EvidenceClass == q.EvidenceClass && d.BidShares <= paperfill.MaxQuantity && d.AskShares <= paperfill.MaxQuantity && !r.OccurredAt.IsZero() && r.AvailableAt.Sub(r.OccurredAt) <= time.Duration(q.MaxQuoteAgeSeconds)*time.Second
}

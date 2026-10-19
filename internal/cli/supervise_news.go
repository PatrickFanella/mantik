package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type newsCatalogFetch func(context.Context) (storage.NewsCatalog, error)
type newsRecordFetch func(context.Context, string, bool) (jsonNewsInspection, error)

var newsRecordKey = regexp.MustCompile(`^[a-f0-9]{64}$`)
var newsEventKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

const newsSupervisionHelp = "Commands (then Enter): n next | p previous | r refresh | d CASE_ID displayed case | e RECORD_KEY excerpts | o RECORD_KEY original (select case first) | q quit"

func fetchNewsCatalog(ctx context.Context, base string) (storage.NewsCatalog, error) {
	var c storage.NewsCatalog
	err := requestWithQuery(ctx, base, "/v1/news-cases", nil, &c, nil, 8<<20)
	return c, err
}
func fetchNewsRecord(ctx context.Context, base, key string, original bool) (jsonNewsInspection, error) {
	var v jsonNewsInspection
	if !newsRecordKey.MatchString(key) {
		return v, errors.New("invalid news record key")
	}
	query := url.Values{}
	if original {
		query.Set("original", "true")
	}
	err := requestWithQuery(ctx, base, "/v1/news-recordings/"+key, nil, &v, query, 4<<20)
	return v, err
}

func validateNewsCatalog(c storage.NewsCatalog) error {
	if !storage.ValidReceiverID(c.ReceiverID) || c.Schema != newscase.Schema || c.Policy != newscase.Policy || c.Cases == nil || len(c.Cases) > newscase.MaxCases {
		return errors.New("invalid news catalogue")
	}
	references := map[string]bool{}
	for i, v := range c.Cases {
		if !newsEventKey.MatchString(v.EventKey) || v.ID != newscase.CaseID(v.EvidenceClass, v.EventKey) || i > 0 && c.Cases[i-1].ID >= v.ID || !slices.Contains([]string{"synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified"}, v.EvidenceClass) || v.Behavior != "research" || !slices.Contains([]string{"open", "closed"}, v.Status) || v.Independence != "not_assessed" || v.Generation < 1 || v.Generation > v.Version || v.Version != len(v.RecordKeys) || v.Version > news.MaxRecords || v.OpenedAt.IsZero() || v.UpdatedAt.Before(v.OpenedAt) {
			return errors.New("invalid news case")
		}
		seen := map[string]bool{}
		for _, key := range v.RecordKeys {
			if !newsRecordKey.MatchString(key) || seen[key] {
				return errors.New("invalid news references")
			}
			seen[key] = true
			references[key] = true
		}
		if v.LastRecordKey != v.RecordKeys[len(v.RecordKeys)-1] {
			return errors.New("invalid latest news reference")
		}
	}
	if len(references) > news.MaxRecords {
		return errors.New("news reference capacity exceeded")
	}
	return nil
}

func validateNewsRecord(v jsonNewsInspection, c newscase.Case, key string, original bool) error {
	index := slices.Index(c.RecordKeys, key)
	if index < 0 || v.RecordKey != key || v.Receipt.RecordKey != key || v.Result.Schema != news.ResultSchema || !newsRecordKey.MatchString(v.Result.RecordingSHA256) || v.Receipt.RecordingSHA256 != v.Result.RecordingSHA256 || v.Receipt.AcceptedAt.IsZero() || v.Receipt.Duplicate || len(v.Result.Claims) < 1 || len(v.Result.Claims) > 16 || len(v.Receipt.Matches) < 1 || len(v.Receipt.Matches) > newscase.MaxCases || (!original && v.Original != "") || original && v.Original == "" {
		return errors.New("invalid targeted news record")
	}
	linked := false
	for _, m := range v.Receipt.Matches {
		if m.CaseID == c.ID && m.Version == index+1 {
			linked = true
		}
	}
	if !linked {
		return errors.New("news record does not match displayed case reference")
	}
	for _, claim := range v.Result.Claims {
		if claim.EvidenceClass != c.EvidenceClass || claim.RecordingSHA256 != v.Result.RecordingSHA256 || claim.Independence != "not_assessed" || !newsRecordKey.MatchString(claim.OriginalSHA256) || newscase.RecordKey(news.Recording{SourceID: claim.SourceID, ArticleID: claim.ArticleID, EvidenceClass: claim.EvidenceClass, Revision: claim.Revision}) != key {
			return errors.New("invalid news claim reference")
		}
		if original {
			if len(v.Original) > 128<<10 || news.SHA256(v.Original) != claim.OriginalSHA256 {
				return errors.New("news original digest mismatch")
			}
			for _, span := range claim.Evidence {
				if span.Start < 0 || span.End <= span.Start || span.End > len(v.Original) || v.Original[span.Start:span.End] != span.Quote {
					return errors.New("news original excerpt mismatch")
				}
			}
		}
	}
	return nil
}

func newsCaseOnPage(c storage.NewsCatalog, offset, limit int, id string) (newscase.Case, bool) {
	for _, v := range c.Cases[offset:min(offset+limit, len(c.Cases))] {
		if v.ID == id {
			return v, true
		}
	}
	return newscase.Case{}, false
}

// News supervision owns only its input and readbacks. It never changes cases,
// starts reviews or imports diagnostic files, and fetches no source by URL.
func superviseNews(ctx context.Context, in io.ReadCloser, out io.Writer, limit int, fetch newsCatalogFetch, inspect newsRecordFetch) error {
	scanner, cleanup := supervisionInput(ctx, in)
	defer cleanup()
	if limit < 1 || limit > 100 {
		return errors.New("news page limit must be 1..100")
	}
	var catalog storage.NewsCatalog
	offset := 0
	read, stale, have := true, true, false
	selected := ""
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if read {
			fresh, err := fetch(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				err = validateNewsCatalog(fresh)
			}
			selected = ""
			if err != nil {
				stale = true
				if _, err = fmt.Fprintln(out, "STALE: news catalogue readback failed or was invalid; use r to refresh or q to quit."); err != nil {
					return err
				}
			} else {
				catalog, offset, stale, have = fresh, 0, false, true
				if _, err = fmt.Fprintln(out, "READBACK OK: news research catalogue (on demand; not a continuous health check)"); err != nil {
					return err
				}
			}
			read = false
		}
		if have {
			if err := renderNewsPage(out, catalog, offset, limit, stale); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out, newsSupervisionHelp); err != nil {
			return err
		}
		if _, err := fmt.Fprint(out, "> "); err != nil {
			return err
		}
		if !scanner.Scan() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return scanner.Err()
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) == 1 && fields[0] == "q" {
			return nil
		}
		if len(fields) == 1 && fields[0] == "r" {
			read = true
			continue
		}
		if stale {
			if _, err := fmt.Fprintln(out, "STALE: news navigation and source reads are unavailable until refresh succeeds."); err != nil {
				return err
			}
			continue
		}
		switch {
		case len(fields) == 1 && fields[0] == "n":
			if offset+limit < len(catalog.Cases) {
				offset += limit
				selected = ""
			} else {
				if _, err := fmt.Fprintln(out, "End of retained news catalogue; r refreshes current state."); err != nil {
					return err
				}
			}
		case len(fields) == 1 && fields[0] == "p":
			offset = max(0, offset-limit)
			selected = ""
		case len(fields) == 2 && fields[0] == "d":
			v, ok := newsCaseOnPage(catalog, offset, limit, fields[1])
			if !ok {
				if _, err := fmt.Fprintln(out, "Choose a news case from this page; no request sent."); err != nil {
					return err
				}
				continue
			}
			selected = v.ID
			if _, err := fmt.Fprintln(out, "DISPLAYED NEWS CASE (catalogue snapshot; r refreshes evidence version and state)"); err != nil {
				return err
			}
			if err := emit(out, v); err != nil {
				return err
			}
		case len(fields) == 2 && (fields[0] == "e" || fields[0] == "o"):
			c, ok := newsCaseOnPage(catalog, offset, limit, selected)
			key := fields[1]
			if !ok || !newsRecordKey.MatchString(key) || !slices.Contains(c.RecordKeys, key) {
				if _, err := fmt.Fprintln(out, "Select a case on this page, then a record key from its displayed references; no request sent."); err != nil {
					return err
				}
				continue
			}
			original := fields[0] == "o"
			view, err := inspect(ctx, key, original)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				err = validateNewsRecord(view, c, key, original)
			}
			if err != nil {
				stale = true
				selected = ""
				if _, err := fmt.Fprintln(out, "STALE: news record request failed or did not match its reference; refresh before continuing."); err != nil {
					return err
				}
				continue
			}
			label := "CURRENT RECORD EXCERPTS"
			if original {
				label = "CURRENT RECORD WITH EXPLICIT ORIGINAL"
			}
			if _, err := fmt.Fprintln(out, label+" (separate readback; case catalogue may have changed)"); err != nil {
				return err
			}
			if err := emit(out, view); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintln(out, "Unknown news command; no request sent."); err != nil {
				return err
			}
		}
	}
}

func renderNewsPage(out io.Writer, c storage.NewsCatalog, offset, limit int, stale bool) error {
	label := "RETAINED NEWS PAGE"
	if stale {
		label = "STALE NEWS PAGE"
	}
	start := offset + 1
	if len(c.Cases) == 0 {
		start = 0
	}
	if _, err := fmt.Fprintf(out, "%s  RECEIVER %s  ITEMS %d..%d OF %d\nID  STATUS  CLASS  EVENT  VERSION  GENERATION  RECORDS  INDEPENDENCE\n", label, strconv.Quote(c.ReceiverID), start, min(offset+limit, len(c.Cases)), len(c.Cases)); err != nil {
		return err
	}
	for _, v := range c.Cases[offset:min(offset+limit, len(c.Cases))] {
		if _, err := fmt.Fprintf(out, "%s  %s  %s  %s  %d  %d  %d  %s\n", strconv.Quote(v.ID), strconv.Quote(v.Status), strconv.Quote(v.EvidenceClass), strconv.Quote(v.EventKey), v.Version, v.Generation, len(v.RecordKeys), strconv.Quote(v.Independence)); err != nil {
			return err
		}
	}
	return nil
}

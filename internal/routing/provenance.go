package routing

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

var symbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validSource(source, symbol, evidence string) bool {
	if source == "fixture" {
		return symbol == "DEMO" && evidence == "synthetic_unqualified"
	}
	return key.MatchString(source) && symbolPattern.MatchString(symbol) && slices.Contains([]string{"synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified"}, evidence)
}

// MarketInputID fences the same source record revision even when clients retry
// it under a different proposed identity. Revisions never overwrite a prior input.
func MarketInputID(i domain.RoutingInput) string {
	if i.Provenance == nil {
		return ""
	}
	raw, _ := json.Marshal([]any{i.Source, i.Symbol, i.EvidenceClass, i.Provenance.RecordKey, i.Provenance.Revision})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("market-%x", sum)
}

func boundedText(s string, max int) bool {
	return s != "" && len(s) <= max && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

func validateProvenance(i domain.RoutingInput) error {
	if i.Source == "fixture" {
		if i.Provenance != nil {
			return errors.New("fixture input cannot claim market provenance")
		}
		return nil
	}
	p := i.Provenance
	if p == nil || !key.MatchString(p.RecordKey) || p.Revision < 1 || p.Revision > 1000000 || !boundedText(p.RawRef, 256) || !digestPattern.MatchString(p.SHA256) || !validTime(p.AvailableAt) || p.AvailableAt.Before(i.OccurredAt) || p.AvailableAt.After(i.ReceivedAt) || !symbolPattern.MatchString(p.SourceSymbol) || !key.MatchString(p.MappingVersion) || !boundedText(p.AccessRef, 256) || !slices.Contains([]string{"usable", "quarantined", "missing"}, p.Quality) {
		return errors.New("market input requires bounded source identity, raw reference, mapping, causal availability and access provenance")
	}
	if (p.Quality != "usable" && !boundedText(p.Reason, 256)) || (p.Quality == "usable" && p.Reason != "") {
		return errors.New("input quality requires a compatible reason")
	}
	if i.ID != MarketInputID(i) {
		return errors.New("market input id must match its source record revision")
	}
	return nil
}

package alpaca

import (
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"strings"
	"time"
)

const CorporateActionFeed = alpacarecord.CorporateActionFeed
const CorporateActionContract = alpacarecord.CorporateActionContract

type CorporateActionRecord = alpacarecord.CorporateActionRecord
type CorporateActionSnapshot = alpacarecord.CorporateActionSnapshot

func validCorporateActionPageToken(token string) bool { return alpacarecord.ValidPageToken(token) }
func corporateActions(r Recording) (*CorporateActionSnapshot, error) {
	return alpacarecord.NormalizeCorporate(alpacarecord.Recording(r))
}

// CorporateActionSnapshots returns every retained snapshot available by the
// explicit cut, in journal receipt order. Later captures never rewrite earlier
// snapshots or imply deletions from an earlier page. Collection completion is
// required for network captures; interrupted acquisitions remain raw evidence.
func (j *Journal) CorporateActionSnapshots(asOf time.Time) ([]CorporateActionSnapshot, error) {
	if !validTime(asOf) {
		return nil, errors.New("corporate-action inspection requires a valid as-of cut")
	}
	captures, err := j.captures()
	if err != nil {
		return nil, err
	}
	out := []CorporateActionSnapshot{}
	for _, c := range captures {
		if c.Result.CorporateActions == nil || c.Recording.ReceivedAt.After(asOf) {
			continue
		}
		// A retained capture can precede the terminal acquisition receipt after
		// a crash. Never promote that artifact into a completed collection.
		snapshot := *c.Result.CorporateActions
		if strings.HasPrefix(c.OriginalRef, "journal:"+j.ID+":collection-") {
			acquisition, err := j.corporateActionAcquisition(c.Recording.CaptureID, &c)
			if errors.Is(err, ErrCollectionIncomplete) || errors.Is(err, ErrCollectionFailed) {
				continue
			}
			if err != nil {
				return nil, err
			}
			requestedAt := acquisition.RequestedAt
			snapshot.RequestedAt = &requestedAt
			snapshot.AvailableAt = acquisition.AvailableAt
			if snapshot.AvailableAt.After(asOf) {
				continue
			}
		}
		out = append(out, snapshot)
	}
	return out, nil
}

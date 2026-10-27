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
			status, err := j.Collection(c.Recording.CaptureID)
			if err != nil {
				return nil, err
			}
			if status.State != "retained" {
				continue
			}
			var intent collectionIntent
			if _, err := j.readCollection(collectionName(c.Recording.CaptureID, "intent"), &intent); err != nil {
				return nil, err
			}
			requestedAt := intent.At.UTC()
			snapshot.RequestedAt = &requestedAt
			var done collectionCompletion
			if _, err := j.readCollection(collectionName(c.Recording.CaptureID, "complete"), &done); err != nil {
				return nil, err
			}
			if done.At.After(snapshot.AvailableAt) {
				snapshot.AvailableAt = done.At
			}
			if snapshot.AvailableAt.After(asOf) {
				continue
			}
		}
		out = append(out, snapshot)
	}
	return out, nil
}

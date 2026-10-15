package alpaca

import (
	"encoding/json"
	"errors"
	"fmt"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

// validatePublicationReceipt binds the server acknowledgement to canonical capture bytes.
func validatePublicationReceipt(r Recording, f feedcollector.Fence, receipt feedcollector.EvidenceReceipt) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if receipt.RegistrationID != f.RegistrationID || receipt.Fence != f || receipt.Key != fmt.Sprintf("%s-r%d", r.CaptureID, r.Revision) || receipt.RecordingSHA256 != Digest(string(canonical)) || !validTime(receipt.PublishedAt) {
		return errors.New("publication receipt did not match retained capture and fence")
	}
	return nil
}

// PublicationRecordings returns only verified canonical journal captures. It
// performs no acquisition, binding change, delivery, or filesystem mutation.
func (j *Journal) PublicationRecordings() ([]Recording, error) {
	captures, err := j.captures()
	if err != nil {
		return nil, err
	}
	out := make([]Recording, 0, len(captures))
	for _, capture := range captures {
		out = append(out, capture.Recording)
	}
	return out, nil
}

package alpaca

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
)

const optionReplyPendingPrefix = "paper-intent-mleg-reply-pending-"

type optionPendingReply struct {
	Name, ClientOrderID, ResponseSHA256 string
}

// Every v2 order reply is fenced before the HTTP request. A crash leaves this
// intent live through rollover; a later convenient GET cannot restore mutation
// authority. No recovery guesses whether the interrupted response was valid.
func (p *PaperTrader) startOptionReply(method, path string, body []byte) (*optionPendingReply, error) {
	var r optionRequest
	if method == http.MethodPost && path == "/v2/orders" {
		if json.Unmarshal(body, &r) != nil || !validOptionRequest(r) {
			return nil, nil // Ordinary stock requests use their existing contract.
		}
		intent, err := p.journal.read(optionIntentName(r.ClientOrderID), MaxBytes)
		if err != nil || !bytes.Equal(intent, body) {
			return nil, ErrJournalConflict
		}
	} else if method == http.MethodGet {
		u, err := url.Parse(path)
		if err != nil || u.Path != "/v2/orders:by_client_order_id" {
			return nil, nil
		}
		id := u.Query().Get("client_order_id")
		if !paperID.MatchString(id) {
			return nil, ErrJournalConflict
		}
		intent, err := p.journal.read(optionIntentName(id), MaxBytes)
		if errors.Is(err, os.ErrNotExist) {
			open, e := p.retainedOpen()
			if e != nil || open.Order.ClientOrderID != id {
				return nil, nil
			}
			r = optionRequest{MLegOrder: open.Order}
		} else if err != nil {
			return nil, err
		} else if json.Unmarshal(intent, &r) != nil || !validOptionRequest(r) {
			return nil, nil
		}
		if r.ClientOrderID != id {
			return nil, ErrJournalConflict
		}
	} else {
		return nil, nil
	}
	open, err := p.retainedOpen()
	if err != nil {
		return nil, err
	}
	if open.ExecutionPolicy != PaperAtomicNetV2 {
		return nil, nil // Legacy work can be inspected, never upgraded.
	}
	pending := &optionPendingReply{Name: optionReplyPendingPrefix + rand.Text() + ".json", ClientOrderID: r.ClientOrderID}
	raw, err := json.Marshal(struct {
		Method, Path string
		Request      optionRequest
	}{method, path, r})
	if err != nil {
		return nil, err
	}
	if err := p.journal.writeOnce(pending.Name, raw); err != nil {
		return nil, err
	}
	p.optionReply = pending
	return pending, nil
}

// Clearing follows durable observation/quarantine, or a known unavailable
// transport response. It never clears an older interrupted request's marker.
func (p *PaperTrader) finishOptionReply(pending *optionPendingReply) error {
	if pending == nil {
		return nil
	}
	if err := p.journal.root.Remove(pending.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := p.journal.syncDirectory(); err != nil {
		return err
	}
	if p.optionReply == pending {
		p.optionReply = nil
	}
	return nil
}

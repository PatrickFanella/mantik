package feedcollector

import "time"

type EvidenceReceipt struct {
	RegistrationID  string    `json:"registration_id"`
	Key             string    `json:"key"`
	Fence           Fence     `json:"fence"`
	PublishedAt     time.Time `json:"published_at"`
	RecordingSHA256 string    `json:"recording_sha256"`
	Duplicate       bool      `json:"duplicate"`
}

package operational

import (
	"encoding/json"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

// Scopes are independently inspectable; fixture readiness excludes real feeds.
var Scopes = []string{"process", "storage", "feed_coverage", "action_delivery", "protective_monitoring", "fixture_operation"}

// Readiness derives current availability without mutating persisted evidence.
func Readiness(state domain.State, now time.Time, storageOK bool, scope string) domain.ReadinessReport {
	report := domain.ReadinessReport{Scope: scope, At: now.UTC(), Mode: "paper_fixture", Capabilities: map[string]domain.CapabilityReadiness{}}
	cap := func(ready bool, status, code string, refs ...string) domain.CapabilityReadiness {
		return domain.CapabilityReadiness{Ready: ready, Status: status, Code: code, EvidenceRefs: refs}
	}
	report.Capabilities["process"] = cap(true, "ready", "http_process_up")
	report.Capabilities["storage"] = cap(storageOK, "ready", "snapshot_available")
	report.Capabilities["feed_coverage"] = cap(false, "not_configured", "real_feed_not_configured")
	if !storageOK {
		report.Capabilities["storage"] = cap(false, "unavailable", "storage_unavailable")
		report.Capabilities["action_delivery"] = cap(false, "unknown", "storage_unavailable")
		report.Capabilities["protective_monitoring"] = cap(false, "unknown", "storage_unavailable")
	} else {
		// A copied state detects missing protection/stalled work immediately, even
		// before the worker persists its next observation of that condition.
		raw, err := json.Marshal(state)
		var observed domain.State
		if err == nil {
			err = json.Unmarshal(raw, &observed)
		}
		if err == nil {
			Sync(&observed, now)
		}
		for _, name := range []string{"action_delivery", "protective_monitoring"} {
			value := cap(false, "not_configured", "worker_not_observed")
			if state.Operations != nil {
				w, ok := state.Operations.Workers[name]
				if ok {
					limit := 10*time.Second + 3*time.Duration(w.IntervalMillis)*time.Millisecond
					switch {
					case now.Before(w.At):
						value = cap(false, "degraded", "clock_backwards", "worker:"+w.WorkerID)
					case w.Stopped:
						value = cap(false, "stopped", "worker_stopped", "worker:"+w.WorkerID)
					case now.Sub(w.At) > limit:
						value = cap(false, "stale", "worker_heartbeat_stale", "worker:"+w.WorkerID)
					case w.ErrorCode != "":
						value = cap(false, "degraded", w.ErrorCode, "worker:"+w.WorkerID)
					default:
						value = cap(true, "ready", "worker_cycle_observed", "worker:"+w.WorkerID)
					}
				}
			}
			if err != nil {
				value = cap(false, "unknown", "state_unreadable")
			} else {
				ids := make([]string, 0, len(observed.Routing.Timers.Exceptions))
				for id := range observed.Routing.Timers.Exceptions {
					ids = append(ids, id)
				}
				slices.Sort(ids)
				for _, id := range ids {
					e := observed.Routing.Timers.Exceptions[id]
					if e.Status == "open" && e.Capability == name {
						value = cap(false, "degraded", e.Code, "exception:"+id)
						break
					}
				}
			}
			report.Capabilities[name] = value
		}
	}
	report.Capabilities["fixture_operation"] = cap(report.Capabilities["storage"].Ready && report.Capabilities["action_delivery"].Ready && report.Capabilities["protective_monitoring"].Ready, "ready", "fixture_capabilities_available")
	if !report.Capabilities["fixture_operation"].Ready {
		report.Capabilities["fixture_operation"] = cap(false, "degraded", "fixture_capability_unavailable")
	}
	report.Ready = report.Capabilities[scope].Ready
	report.Status = "not_ready"
	if report.Ready {
		report.Status = "ready"
	}
	return report
}

// Inspect combines persisted exception records with fresh readiness derivation.
func Inspect(state domain.State, now time.Time) domain.OperationsSnapshot {
	workers := map[string]domain.WorkerHealth{}
	if state.Operations != nil {
		workers = state.Operations.Workers
	}
	exceptions := []domain.OperationalException{}
	if state.Routing != nil && state.Routing.Timers != nil {
		for _, e := range state.Routing.Timers.Exceptions {
			exceptions = append(exceptions, e)
		}
	}
	slices.SortFunc(exceptions, func(a, b domain.OperationalException) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return domain.OperationsSnapshot{Readiness: Readiness(state, now, true, "storage"), Workers: workers, Exceptions: exceptions}
}

// Package workerreceipt validates supplied worker receipt structures and resource
// profiles. It performs no process, filesystem, network or Docker operations.
package workerreceipt

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

var ErrConflict = errors.New("worker launch identity, artifact or ownership conflict")
var launchID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type Profile struct {
	JobSHA256, ImageSHA256, AcquisitionSchema string
	Resources                                 workerplan.Resources
	Entrypoint, Args, Env                     []string
	BehaviorTmp                               bool
}
type Acquisition struct {
	Schema        string    `json:"schema"`
	ID            string    `json:"id"`
	Root          string    `json:"root"`
	JobSHA256     string    `json:"job_sha256"`
	ImageSHA256   string    `json:"image_sha256"`
	ContainerName string    `json:"container_name"`
	CreatedAt     time.Time `json:"created_at"`
	Deadline      time.Time `json:"deadline"`
	SHA256        string    `json:"sha256"`
}
type PreStart struct {
	AcquisitionSHA256 string
	Snapshot          Snapshot
	SHA256            string
}
type Receipt struct {
	Schema              string    `json:"schema"`
	AcquisitionSHA256   string    `json:"acquisition_sha256"`
	ContainerID         string    `json:"container_id,omitempty"`
	ImageSHA256         string    `json:"image_sha256"`
	Status              string    `json:"status"`
	ReasonCode          string    `json:"reason_code"`
	ExitCode            int       `json:"exit_code"`
	FinishedAt          time.Time `json:"finished_at,omitzero"`
	CollectedAt         time.Time `json:"collected_at"`
	ProfileVerified     bool      `json:"profile_verified"`
	HostDeadlineMet     bool      `json:"host_deadline_met"`
	OutputComplete      bool      `json:"output_complete"`
	StdoutSHA256        string    `json:"stdout_sha256"`
	StdoutBytes         int       `json:"stdout_bytes"`
	StderrSHA256        string    `json:"stderr_sha256"`
	StderrBytes         int       `json:"stderr_bytes"`
	WorkerResultSHA256  string    `json:"worker_result_sha256,omitempty"`
	WorkerStatus        string    `json:"worker_status,omitempty"`
	Authority           string    `json:"authority"`
	QualificationCredit bool      `json:"qualification_credit"`
	SHA256              string    `json:"sha256"`
}

type Snapshot struct {
	ID     string `json:"Id"`
	Name   string
	Image  string
	Config struct {
		User       string
		Env        []string
		Entrypoint []string
		Cmd        []string
		Labels     map[string]string
		Tty        bool
	}
	HostConfig struct {
		Tmpfs          map[string]string
		ReadonlyRootfs bool
		NetworkMode    string
		CapDrop        []string
		CapAdd         []string
		SecurityOpt    []string
		Memory         int64
		MemorySwap     int64
		NanoCpus       int64
		PidsLimit      int64
		Privileged     bool
		PidMode        string
		IpcMode        string
		UTSMode        string
		UsernsMode     string
		Binds          []string
		Devices        []json.RawMessage
		PortBindings   map[string]json.RawMessage
		RestartPolicy  struct{ Name string }
		LogConfig      struct {
			Type   string
			Config map[string]string
		}
	}
	Mounts []struct {
		Source      string
		Destination string
		RW          bool
		Type        string
	}
	State struct {
		Running    bool
		Pid        int
		ExitCode   int
		Status     string
		StartedAt  time.Time
		FinishedAt time.Time
	}
}

func (a Acquisition) Validate(root string, p Profile, approved string) error {
	expected := a.SHA256
	a.SHA256 = ""
	if a.Schema != p.AcquisitionSchema || a.Root != root || !launchID.MatchString(a.ID) || a.ContainerName != "mantik-worker-"+strings.ToLower(a.ID) || a.JobSHA256 != p.JobSHA256 || a.ImageSHA256 != approved || approved != p.ImageSHA256 || a.CreatedAt.IsZero() || a.CreatedAt.Year() < 1 || a.CreatedAt.Year() > 9999 || !a.Deadline.Equal(a.CreatedAt.Add(time.Duration(p.Resources.WallSeconds)*time.Second)) || expected != workerplan.Hash(a) {
		return ErrConflict
	}
	return nil
}
func VerifyProfile(s Snapshot, a Acquisition, p Profile) error {
	h, c := s.HostConfig, s.Config
	r := p.Resources
	if s.Image != p.ImageSHA256 || c.User != "65532:65532" || c.Tty || !reflect.DeepEqual(c.Entrypoint, p.Entrypoint) || !reflect.DeepEqual(c.Cmd, p.Args) || !reflect.DeepEqual(c.Env, p.Env) || !h.ReadonlyRootfs || h.NetworkMode != "none" || !reflect.DeepEqual(h.CapDrop, []string{"ALL"}) || len(h.CapAdd) != 0 || !reflect.DeepEqual(h.SecurityOpt, []string{"no-new-privileges:true"}) || h.Memory != r.MemoryBytes || h.MemorySwap != r.MemoryBytes || h.NanoCpus != int64(r.CPUMillis)*1000000 || h.PidsLimit != int64(r.PIDs) || h.Privileged || h.PidMode != "" || h.IpcMode != "private" || h.UTSMode != "" || h.UsernsMode != "" || len(h.Binds) != 0 || len(h.Devices) != 0 || len(h.PortBindings) != 0 || h.RestartPolicy.Name != "no" || h.LogConfig.Type != "local" || !reflect.DeepEqual(h.LogConfig.Config, map[string]string{"max-size": "20m", "max-file": "1", "compress": "false"}) {
		return workerplan.ErrInvalid
	}
	expectedTmp := map[string]string(nil)
	if p.BehaviorTmp {
		expectedTmp = map[string]string{"/tmp": "rw,exec,nosuid,nodev,size=67108864,mode=1777"}
	}
	if len(h.Tmpfs) != len(expectedTmp) || (p.BehaviorTmp && !reflect.DeepEqual(h.Tmpfs, expectedTmp)) {
		return workerplan.ErrInvalid
	}
	bind, tmp := 0, 0
	for _, m := range s.Mounts {
		if m.Source == a.Root+"/input" && m.Destination == "/worker" && !m.RW && m.Type == "bind" {
			bind++
			continue
		}
		if p.BehaviorTmp && m.Source == "" && m.Destination == "/tmp" && m.RW && m.Type == "tmpfs" {
			tmp++
			continue
		}
		return workerplan.ErrInvalid
	}
	if bind != 1 || tmp > 1 {
		return workerplan.ErrInvalid
	}
	return nil
}

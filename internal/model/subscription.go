// Package model runs bounded research prompts through locally authenticated subscription CLIs.
package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	ErrBudgetExhausted = errors.New("subscription daily budget exhausted")
	ErrBackoff         = errors.New("subscription model is rate-limited")
	ErrBusy            = errors.New("subscription model journal is busy")
)

type Provider string

const (
	Claude Provider = "claude"
	Codex  Provider = "codex"
)

type Call struct {
	Provider Provider
	Prompt   string
	Timeout  time.Duration
}
type Record struct {
	// Response is available only to the immediate caller, never JSON or journal output.
	Response       string    `json:"-"`
	Provider       Provider  `json:"provider"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	PromptSHA256   string    `json:"prompt_sha256"`
	ResponseSHA256 string    `json:"response_sha256,omitempty"`
	Status         string    `json:"status"`
}
type budget struct {
	Date       string    `json:"date"`
	Calls      int       `json:"calls"`
	RetryAfter time.Time `json:"retry_after,omitempty"`
}
type Journal struct {
	root string
	now  func() time.Time
	run  func(context.Context, string, string, ...string) ([]byte, error)
}

func OpenJournal(root string) (*Journal, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("model journal requires an absolute private directory")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("cannot create model journal")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !owned(info) {
		return nil, errors.New("model journal requires mode 0700 non-symlink directory")
	}
	return &Journal{root: root, now: time.Now, run: run}, nil
}
func run(ctx context.Context, name, prompt string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "mantik-subscription-")
	if err != nil {
		return nil, errors.New("cannot create isolated model directory")
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command(name, args...)
	cmd.Env = cleanEnv(os.Environ())
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output, diagnostics limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostics
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if output.exceeded || diagnostics.exceeded {
			return output.Bytes(), errors.New("subscription output exceeds 65536 bytes")
		}
		if err != nil && (rateLimited(diagnostics.Bytes()) || rateLimited(output.Bytes())) {
			return output.Bytes(), errors.New("subscription rate limit")
		}
		return output.Bytes(), err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return output.Bytes(), ctx.Err()
	}
}

type limitedBuffer struct {
	raw      []byte
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 65536 - len(b.raw)
	if remaining <= 0 {
		b.exceeded = true
		return n, nil
	}
	if len(p) > remaining {
		b.raw = append(b.raw, p[:remaining]...)
		b.exceeded = true
	} else {
		b.raw = append(b.raw, p...)
	}
	return n, nil
}
func (b *limitedBuffer) Bytes() []byte { return append([]byte(nil), b.raw...) }
func rateLimited(raw []byte) bool {
	value := strings.ToLower(string(raw))
	return strings.Contains(value, "rate limit") || strings.Contains(value, "too many requests") || strings.Contains(value, "429") || strings.Contains(value, "weekly limit") || strings.Contains(value, "usage limit") || strings.Contains(value, "you've hit your")
}
func cleanEnv(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		if !(strings.Contains(key, "API_KEY") || strings.Contains(key, "APIKEY") || strings.Contains(key, "AUTH_TOKEN") || strings.Contains(key, "ACCESS_TOKEN") || strings.HasSuffix(key, "OPENAI_KEY") || strings.HasSuffix(key, "ANTHROPIC_KEY")) {
			result = append(result, entry)
		}
	}
	return result
}
func digest(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }

func (j *Journal) Call(ctx context.Context, call Call, dailyBudget int) (Record, error) {
	if dailyBudget < 1 || (call.Provider != Claude && call.Provider != Codex) || strings.TrimSpace(call.Prompt) == "" || len(call.Prompt) > 32768 {
		return Record{}, errors.New("invalid subscription model call")
	}
	if call.Timeout <= 0 || call.Timeout > 2*time.Minute {
		return Record{}, errors.New("model timeout must be between zero and two minutes")
	}
	unlock, err := j.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	now := j.now().UTC()
	state, err := j.loadBudget()
	if err != nil {
		return Record{}, err
	}
	date := now.Format("2006-01-02")
	if state.Date == "" {
		state.Date = date
	}
	if state.Date > date {
		return Record{}, errors.New("model journal clock moved backwards")
	}
	if state.Date < date {
		state.Date, state.Calls = date, 0
	}
	if state.RetryAfter.After(now) {
		return Record{}, ErrBackoff
	}
	if state.Calls < 0 || state.Calls > dailyBudget {
		return Record{}, errors.New("invalid model budget")
	}
	if state.Calls >= dailyBudget {
		return Record{}, ErrBudgetExhausted
	}
	state.Calls++
	if err = j.saveBudget(state); err != nil {
		return Record{}, err
	}
	record := Record{Provider: call.Provider, StartedAt: now, PromptSHA256: digest(call.Prompt), Status: "started"}
	if err = j.append(record); err != nil {
		return Record{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, call.Timeout)
	defer cancel()
	var output []byte
	if call.Provider == Claude {
		output, err = j.run(bounded, "claude", call.Prompt, "-p", "--restricted", "--safe-mode", "--strict-mcp-config", "--no-session-persistence", "--tools", "")
	} else {
		output, err = j.run(bounded, "codex", call.Prompt, "exec", "--sandbox", "read-only", "--ephemeral", "--ignore-rules", "--ignore-user-config", "--skip-git-repo-check", "-")
	}
	record.FinishedAt = j.now().UTC()
	record.ResponseSHA256 = digest(string(output))
	record.Status = "failed"
	if err == nil {
		record.Status = "completed"
		record.Response = string(output)
	} else if strings.Contains(strings.ToLower(err.Error()), "rate limit") || strings.Contains(strings.ToLower(err.Error()), "429") {
		record.Status = "rate_limited"
		state.RetryAfter = record.FinishedAt.Add(5 * time.Minute)
		if saveErr := j.saveBudget(state); saveErr != nil {
			return Record{}, saveErr
		}
	}
	if appendErr := j.append(record); appendErr != nil {
		return Record{}, appendErr
	}
	if err != nil {
		return record, fmt.Errorf("%s subscription call failed: %w", call.Provider, err)
	}
	return record, nil
}
func (j *Journal) lock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(j.root, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("cannot lock model journal")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, errors.New("cannot lock model journal")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
func (j *Journal) loadBudget() (budget, error) {
	f, err := os.OpenFile(filepath.Join(j.root, "budget.json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return budget{}, nil
	}
	if err != nil {
		return budget{}, errors.New("cannot read model budget")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !owned(info) || info.Mode().Perm() != 0600 {
		return budget{}, errors.New("invalid private model budget")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1025))
	if errors.Is(err, os.ErrNotExist) {
		return budget{}, nil
	}
	if err != nil || len(raw) > 1024 {
		return budget{}, errors.New("cannot read model budget")
	}
	var state budget
	if json.Unmarshal(raw, &state) != nil || state.Calls < 0 {
		return budget{}, errors.New("invalid model budget")
	}
	if _, err := time.Parse("2006-01-02", state.Date); err != nil {
		return budget{}, errors.New("invalid model budget date")
	}
	return state, nil
}
func (j *Journal) saveBudget(state budget) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(j.root, "budget-")
	if err != nil {
		return errors.New("cannot write model budget")
	}
	name := f.Name()
	defer os.Remove(name)
	if err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(j.root, "budget.json"))
	}
	if err == nil {
		dir, openErr := os.Open(j.root)
		if openErr == nil {
			err = dir.Sync()
			_ = dir.Close()
		} else {
			err = openErr
		}
	}
	if err != nil {
		return errors.New("cannot write model budget")
	}
	return nil
}
func (j *Journal) append(record Record) error {
	f, err := os.OpenFile(filepath.Join(j.root, "calls.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("cannot append model call record")
	}
	defer f.Close()
	raw, _ := json.Marshal(record)
	if _, err = io.WriteString(f, string(raw)+"\n"); err != nil {
		return errors.New("cannot append model call record")
	}
	if err = f.Sync(); err != nil {
		return errors.New("cannot append model call record")
	}
	return nil
}

func owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Getuid())
}

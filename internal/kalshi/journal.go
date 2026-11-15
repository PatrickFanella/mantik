package kalshi

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// OpenClient retains public HTTP responses in a private journal before parsing.
// The returned client owns that journal until Close. No credentials are accepted.
func OpenClient(path string) (*Client, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("Kalshi journal requires an absolute path")
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || st.Uid != uint32(os.Getuid()) {
		return nil, errors.New("Kalshi journal requires a private current-user directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	c := NewClient()
	c.journal = root
	return c, nil
}

// Close releases the local capture directory. It never deletes retained bytes.
func (c *Client) Close() error {
	if c.journal == nil {
		return nil
	}
	return c.journal.Close()
}

func (c *Client) capture(path string, status int, raw []byte, at time.Time, partial bool) error {
	if c.journal == nil {
		return nil
	} // Low-level fetches can be inspected without collection.
	data, err := json.Marshal(struct {
		Path       string
		Status     int
		ObservedAt time.Time
		Partial    bool
		SHA256     string
		Raw        []byte
	}{path, status, at, partial, digest(raw), raw})
	if err != nil {
		return err
	}
	name := "capture-" + rand.Text() + ".json"
	f, err := c.journal.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	dir, err := c.journal.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

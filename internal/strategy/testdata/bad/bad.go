// Package bad is an audit fixture that breaks every rule.
package bad

import (
	"net/http"
	"os"
	clock "time"
)

func peek() {
	_, _ = http.Get("https://example.invalid")
	_ = os.Getenv("HOME")
	now := clock.Now
	_ = now()
	clock.Sleep(1)
}

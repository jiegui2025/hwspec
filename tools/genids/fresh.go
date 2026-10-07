package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jiegui2025/hwspec/internal/ids"
)

// clock is the release guard's time; tests replace it.
var clock = time.Now

// fresh fails when dir's manifest was generated more than days ago, or
// more than a day in the future (#17): the release workflow runs it on
// internal/ids/data, so a release doesn't ship ID databases months old.
// `make update-ids` refreshes them.
func fresh(stdout io.Writer, dir, days string) error {
	n, err := strconv.Atoi(days)
	if err != nil || n <= 0 {
		return fmt.Errorf("days %q isn't a positive number", days)
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	m, err := ids.ParseManifest(b)
	if err != nil {
		return err
	}
	at, now := m.GeneratedAt.UTC(), clock().UTC()
	switch age := now.Sub(at); {
	case age > time.Duration(n)*24*time.Hour:
		return fmt.Errorf("the embedded ID databases were generated %s, %d days ago (at most %d): run make update-ids and merge the result",
			at.Format(time.DateOnly), int(age.Hours()/24), n)
	case age < -24*time.Hour:
		return fmt.Errorf("the embedded ID databases' generated_at %s is in the future", at.Format(time.RFC3339))
	}
	fmt.Fprintf(stdout, "embedded ID databases generated %s, within %d days\n", at.Format(time.DateOnly), n)
	return nil
}

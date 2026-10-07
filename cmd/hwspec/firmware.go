package main

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/jiegui2025/hwspec/internal/fwindex"
)

// updateFirmware fetches, verifies and installs the firmware index; tests
// replace it.
var updateFirmware = fwindex.Update

func (c cli) firmwareCmd(args []string) error {
	const use = "usage: hwspec firmware update [--dry-run] [--allow-older]"
	if len(args) == 0 || args[0] != "update" {
		return errors.New(use)
	}
	fs := newFlags("firmware update")
	var dryRun, allowOlder bool
	fs.BoolVar(&dryRun, "dry-run", false, "")
	fs.BoolVar(&allowOlder, "allow-older", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New(use)
	}
	if underSudo() {
		return errors.New("run `hwspec firmware update` without sudo: the index is kept in your own cache and needs no root")
	}
	dir := fwindex.CacheDir()
	fmt.Fprintln(c.stderr, "Checking cdn.fwupd.org (LVFS) and git.kernel.org (linux-firmware)")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := updateFirmware(ctx, fwindex.Options{
		Dir:        dir,
		UserAgent:  "hwspec/" + fullVersion(),
		DryRun:     dryRun,
		AllowOlder: allowOlder,
	})
	if res == nil {
		return fmt.Errorf("firmware update failed, nothing changed: %w", err)
	}
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	missed := 0
	for _, r := range res {
		status := r.Status
		switch {
		case r.Status == fwindex.Updated && dryRun:
			status = "would update"
		case r.Status == fwindex.Refused || r.Status == fwindex.Failed:
			status += " (" + r.Reason + ")"
			missed++
		}
		date := ""
		switch {
		case r.Tag != "":
			date = "release " + r.Tag
		case !r.Date.IsZero():
			date = "signed " + r.Date.UTC().Format("2006-01-02 15:04 UTC")
		}
		if r.Components > 0 {
			date += fmt.Sprintf(", %d components", r.Components)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Source, date, status)
	}
	w.Flush()
	for _, r := range res {
		if r.Warning != "" {
			fmt.Fprintf(c.stderr, "hwspec: warning: %s: %s\n", r.Source, r.Warning)
		}
	}
	switch {
	case err != nil:
		return fmt.Errorf("firmware update verified but not installed: %w", err)
	case missed > 0:
		return fmt.Errorf("%d of %d sources not updated; the cached copy of each stays as it was", missed, len(res))
	case dryRun:
		fmt.Fprintln(c.stdout, "\nNothing written (--dry-run).")
	default:
		fmt.Fprintf(c.stdout, "\nFirmware index in %s.\n", dir)
	}
	return nil
}

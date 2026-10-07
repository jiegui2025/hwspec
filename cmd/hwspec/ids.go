package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jiegui2025/hwspec/internal/ids"
)

func (c cli) idsCmd(args []string) error {
	if len(args) == 0 {
		return c.idsStatus()
	}
	switch args[0] {
	case "lookup":
		if len(args) < 3 {
			return errors.New("usage: hwspec ids lookup KIND ID   (e.g. pci 8086:3e92, usb class 03, jedec F785)")
		}
		kind := ids.Kind(strings.ToLower(args[1]))
		id := strings.Join(args[2:], " ")
		name, key, err := ids.Lookup(kind, id)
		if err != nil {
			return err
		}
		if name == "" {
			fmt.Fprintf(c.stdout, "%s %s: not found (key %s)\n", kind, id, key)
			return errNotFound
		}
		fmt.Fprintf(c.stdout, "%s\t(%s %s)\n", name, kind, key)
		return nil
	case "template":
		if len(args) > 1 {
			return errors.New("usage: hwspec ids template")
		}
		fmt.Fprint(c.stdout, ids.OverridesHelp)
		return nil
	case "update":
		return c.idsUpdate(args[1:])
	}
	return fmt.Errorf("unknown ids subcommand %q (want update, lookup or template)", args[0])
}

// underSudo reports a run as root through sudo. The commands that keep
// the user's own data (ids update, firmware update) need no root, and as
// root would write into root's home, or, with sudo -E, make the user's own
// directories root's.
func underSudo() bool { return geteuid() == 0 && os.Getenv("SUDO_UID") != "" }

// updateIDs downloads and installs the signed bundle; tests replace it.
var updateIDs = ids.Update

func (c cli) idsUpdate(args []string) error {
	fs := newFlags("ids update")
	var check, allowOlder bool
	url := os.Getenv("HWSPEC_IDS_URL")
	fs.BoolVar(&check, "check", false, "")
	fs.BoolVar(&allowOlder, "allow-older", false, "")
	fs.StringVar(&url, "url", url, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if underSudo() {
		return errors.New("run `hwspec ids update` without sudo: the databases are kept in your own data directory and need no root")
	}
	if url == "" {
		url = ids.DefaultSyncURL
	}
	fmt.Fprintf(c.stderr, "Checking %s\n", url)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := updateIDs(ctx, ids.UpdateOptions{
		BaseURL:    url,
		UserAgent:  "hwspec/" + fullVersion(),
		DryRun:     check,
		AllowOlder: allowOlder,
	})
	var partial *ids.InstallError
	switch {
	case errors.Is(err, ids.ErrBuiltInIsNewer):
		fmt.Fprintf(c.stdout, "Already up to date: the databases built into hwspec are newer than the published bundle (%s).\n", res.BundleAt.Format("2006-01-02"))
		return nil
	case errors.As(err, &partial):
		return fmt.Errorf("update partly installed: %w; run `hwspec ids update` again to finish", err)
	case err != nil:
		return fmt.Errorf("update failed, nothing changed: %w", err)
	}
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	changed := 0
	results := res.Files
	for _, r := range results {
		status := r.Status
		if check && status != "unchanged" {
			status = "would update"
		}
		if r.Status != "unchanged" {
			changed++
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d names\n", r.File, status, r.Date, r.Entries)
	}
	w.Flush()
	switch {
	case check:
		fmt.Fprintf(c.stdout, "\n%d of %d databases have updates. Run `hwspec ids update` to install them.\n", changed, len(results))
	case changed == 0:
		fmt.Fprintln(c.stdout, "\nAlready up to date.")
	default:
		fmt.Fprintf(c.stdout, "\nInstalled %d databases into %s.\n", changed, ids.SyncedDir())
	}
	// Bundles are published weekly; a much older one means publishing has
	// stopped (or a mirror is stale).
	if time.Since(res.BundleAt) > 60*24*time.Hour {
		fmt.Fprintf(c.stderr, "hwspec: warning: the newest published bundle is from %s; the ID databases may be stale\n", res.BundleAt.Format("2006-01-02"))
	}
	return nil
}

func (c cli) idsStatus() error {
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DATABASE\tNAMES\tSOURCE (newest of embedded, distro, synced; then overrides)")
	ids.Preload(ids.Kinds...) // all of them are listed: load them in parallel, not one by one
	for _, k := range ids.Kinds {
		var parts []string
		for _, l := range ids.Layers(k) {
			s := l.Source
			if l.Date != "" {
				s += " " + l.Date
			}
			if l.Source == ids.OverridesPath() {
				s = fmt.Sprintf("overrides (%d)", l.Entries)
			}
			if l.Err != "" {
				s += " (unusable: " + l.Err + ")"
			}
			parts = append(parts, s)
		}
		fmt.Fprintf(w, "%s\t%d\t%s\n", k, ids.Entries(k), strings.Join(parts, "  →  "))
	}
	// The advisor's knowledge base travels in the same bundle (#81).
	if k, source, warns, err := knowledgeBase(); err == nil {
		s := source + " " + k.Version
		if len(warns) > 0 {
			s += " (" + strings.Join(warns, "; ") + ")"
		}
		fmt.Fprintf(w, "advisor\t%d\t%s\n", len(k.Rules), s)
	}
	w.Flush()

	switch t, err := ids.SyncedAt(); {
	case err != nil:
		fmt.Fprintf(c.stdout, "\nSynced databases are not used: their manifest is unreadable (%v).\nRun `hwspec ids update --allow-older` to replace them.\n", err)
	case t.IsZero():
		fmt.Fprintln(c.stdout, "\nNot synced yet. `hwspec ids update` downloads the latest databases (signed, ~1 MB).")
	default:
		fmt.Fprintf(c.stdout, "\nSynced bundle: built %s, in %s\n", t.Format("2006-01-02"), ids.SyncedDir())
	}
	if err := ids.OverridesError(); err != nil {
		fmt.Fprintf(c.stdout, "Overrides file has problems (valid lines still apply): %v\n", err)
	}

	path := ids.OverridesPath()
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(c.stdout, "Overrides: %s\n", path)
	} else {
		fmt.Fprintf(c.stdout, "No overrides file. To correct or add names, create %s\n(start from `hwspec ids template`).\n", path)
	}
	return nil
}

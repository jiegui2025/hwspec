package advisor

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// WriteText prints advice for people: most severe first, each finding with
// what triggered it, what to do and where the advice comes from. Every
// string is cleaned of control characters: captures and knowledge-base
// bundles are downloaded files.
func WriteText(w io.Writer, a Advice) error {
	c := report.CleanString
	bw := bufio.NewWriter(w)
	where := "saved capture"
	if a.Live {
		where = "this machine"
	}
	fmt.Fprintf(bw, "hwspec advice  ·  %s  ·  knowledge base %s\n", where, c(a.KBVersion))
	gaps := 0
	for _, w := range a.Warnings {
		if strings.HasPrefix(w, "capture: ") {
			gaps++
		}
	}
	if len(a.Findings) == 0 {
		total := a.RulesApplied + a.RulesSkipped
		if a.RulesSkipped == 0 {
			fmt.Fprintf(bw, "\nNothing found by the %s.\n", rules(total))
		} else {
			fmt.Fprintf(bw, "\nNothing found by %d of %s; %d couldn't be applied (see Warnings).\n", a.RulesApplied, rules(total), a.RulesSkipped)
		}
		if gaps > 0 {
			fmt.Fprintf(bw, "The capture couldn't read everything (%d warnings below), which can hide findings.\n", gaps)
		}
	}
	for _, f := range a.Findings {
		fmt.Fprintf(bw, "\n%s  %s  (%s, %s)\n", strings.ToUpper(c(string(f.Severity))), c(f.Title), c(string(f.Category)), c(f.ID))
		if f.Device != nil {
			fmt.Fprintf(bw, "  Device     %s %s", c(f.Device.Kind), c(f.Device.Key))
			if f.Device.Name != "" {
				fmt.Fprintf(bw, "  %s", c(f.Device.Name))
			}
			fmt.Fprintln(bw)
		}
		if f.Detail != "" {
			fmt.Fprintf(bw, "  Why        %s\n", c(f.Detail))
		}
		for _, an := range f.Answers {
			fmt.Fprintf(bw, "  %-10s %s\n", cmp.Or(topics[an.Topic], "Answer"), c(an.Text))
		}
		for _, e := range f.Evidence {
			v := "absent"
			if value, ok := e.Value(); ok {
				v = fmt.Sprint(value)
			}
			fmt.Fprintf(bw, "  Evidence   %s = %s\n", c(e.Path()), c(v))
		}
		for _, act := range f.Actions {
			label := "To do"
			if act.Distro != "" {
				label = c(act.Distro)
			}
			fmt.Fprintf(bw, "  %-10s %s\n", label, c(act.Text))
			for _, cmd := range act.Commands {
				fmt.Fprintf(bw, "             $ %s\n", c(cmd))
			}
			if act.Risk != "" {
				fmt.Fprintf(bw, "  Risk       %s\n", c(act.Risk))
			}
			if act.Undo != "" {
				fmt.Fprintf(bw, "  Undo       %s\n", c(act.Undo))
			}
		}
		for _, s := range f.Sources {
			fmt.Fprintf(bw, "  Source     %s  %s (%s, %s, retrieved %s)\n", c(s.ID), c(s.URL), c(string(s.Confidence)), c(s.Licence), c(s.Retrieved))
		}
		if f.Confidence != "" {
			fmt.Fprintf(bw, "  Confidence %s\n", c(string(f.Confidence)))
		}
	}
	if len(a.Warnings) > 0 {
		fmt.Fprintln(bw, "\nWarnings")
		for _, warn := range a.Warnings {
			fmt.Fprintf(bw, "  %s\n", c(warn))
		}
	}
	return bw.Flush()
}

// topics labels an answer's lines; a topic a newer build added is shown
// as "Answer".
var topics = map[string]string{
	"slots": "Slots", "channels": "Channels", "pairs": "Pairs", "speed": "Speed", "faster": "Faster",
	"minimum_speed": "Min speed", "max_capacity": "Max total", "fits": "Fits",
}

func rules(n int) string {
	if n == 1 {
		return "1 rule"
	}
	return fmt.Sprintf("%d rules", n)
}

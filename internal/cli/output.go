package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aravindakrc/toolbox/internal/engine"
)

func printReport(w io.Writer, r *engine.Report, format string) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}

	fmt.Fprintln(w, r.Object)
	for _, f := range r.Fields {
		switch {
		case f.Missing:
			fmt.Fprintf(w, "  %-14s <missing>\n", f.Name)
		case f.From != "":
			fmt.Fprintf(w, "  %-14s %s  (%s)\n", f.Name, f.Value, f.From)
		default:
			fmt.Fprintf(w, "  %-14s -\n", f.Name)
		}
	}
	for slot, target := range r.Uses {
		fmt.Fprintf(w, "  uses %-9s %s\n", slot, target)
	}
	for name, value := range r.Outputs {
		fmt.Fprintf(w, "  out  %-9s %s\n", name, value)
	}
	if len(r.Actions) > 0 {
		fmt.Fprintf(w, "  result: %s\n", strings.Join(r.Actions, ", "))
	}
	if r.Verified != nil {
		fmt.Fprintf(w, "  verified: %t\n", *r.Verified)
	}
	for _, n := range r.Needs {
		fmt.Fprintf(w, "  needs %s: %s\n", n.Name, n.Message)
	}
	return nil
}

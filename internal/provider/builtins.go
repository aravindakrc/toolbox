package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

// Portable scripts get a small set of utilities that behave the same on every
// operating system. They deliberately support only the options listed here.
type builtin struct {
	// options lists the single-letter flags the command accepts.
	options string
	run     func(hc interp.HandlerContext, flags map[rune]bool, operands []string) error
}

var utilities = map[string]builtin{
	"mkdir": {options: "p", run: mkdir},
	"rm":    {options: "rf", run: remove},
	"cp":    {run: copyFile},
	"cat":   {run: cat},
	"ls":    {run: list},
}

// builtins routes utility calls to the implementations above and everything
// else to the real program on PATH.
func builtins(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		util, ok := utilities[args[0]]
		if !ok {
			return next(ctx, args)
		}
		hc := interp.HandlerCtx(ctx)
		flags, operands, err := parseOptions(args[1:], util.options)
		if err == nil {
			err = util.run(hc, flags, operands)
		}
		if err != nil {
			fmt.Fprintf(hc.Stderr, "%s: %v\n", args[0], err)
			return interp.NewExitStatus(1)
		}
		return nil
	}
}

// parseOptions splits args into single-letter flags (such as -rf) and operands.
func parseOptions(args []string, allowed string) (map[rune]bool, []string, error) {
	flags := map[rune]bool{}
	var operands []string
	for i, a := range args {
		if a == "--" {
			return flags, append(operands, args[i+1:]...), nil
		}
		if len(a) < 2 || a[0] != '-' {
			operands = append(operands, a)
			continue
		}
		for _, c := range a[1:] {
			if !strings.ContainsRune(allowed, c) {
				return nil, nil, fmt.Errorf("unsupported option -%c", c)
			}
			flags[c] = true
		}
	}
	return flags, operands, nil
}

func resolve(hc interp.HandlerContext, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(hc.Dir, p)
}

func mkdir(hc interp.HandlerContext, flags map[rune]bool, operands []string) error {
	if len(operands) == 0 {
		return errors.New("missing operand")
	}
	for _, p := range operands {
		var err error
		if flags['p'] {
			err = os.MkdirAll(resolve(hc, p), 0o755)
		} else {
			err = os.Mkdir(resolve(hc, p), 0o755)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func remove(hc interp.HandlerContext, flags map[rune]bool, operands []string) error {
	if len(operands) == 0 {
		return errors.New("missing operand")
	}
	for _, p := range operands {
		path := resolve(hc, p)
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist) && flags['f']:
			continue
		case err != nil:
			return err
		case info.IsDir() && !flags['r']:
			return fmt.Errorf("%s is a directory, use -r", p)
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(hc interp.HandlerContext, _ map[rune]bool, operands []string) error {
	if len(operands) != 2 {
		return errors.New("usage: cp source destination")
	}
	src, dst := resolve(hc, operands[0]), resolve(hc, operands[1])
	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", operands[0])
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm())
}

func cat(hc interp.HandlerContext, _ map[rune]bool, operands []string) error {
	if len(operands) == 0 {
		if hc.Stdin == nil {
			return nil
		}
		_, err := io.Copy(hc.Stdout, hc.Stdin)
		return err
	}
	for _, p := range operands {
		data, err := os.ReadFile(resolve(hc, p))
		if err != nil {
			return err
		}
		if _, err := hc.Stdout.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func list(hc interp.HandlerContext, _ map[rune]bool, operands []string) error {
	if len(operands) == 0 {
		operands = []string{"."}
	}
	for _, p := range operands {
		path := resolve(hc, p)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			fmt.Fprintln(hc.Stdout, p)
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintln(hc.Stdout, n)
		}
	}
	return nil
}

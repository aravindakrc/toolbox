package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// terminalPrompter asks questions on a text terminal.
type terminalPrompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newTerminalPrompter(in io.Reader, out io.Writer) *terminalPrompter {
	return &terminalPrompter{in: bufio.NewReader(in), out: out}
}

func (p *terminalPrompter) Ask(_ context.Context, question string) (string, error) {
	fmt.Fprintf(p.out, "%s: ", question)
	return p.readLine()
}

func (p *terminalPrompter) Choose(_ context.Context, question string, options []string) (string, error) {
	if len(options) == 0 {
		return "", errors.New("nothing to choose from")
	}
	fmt.Fprintln(p.out, question)
	for i, option := range options {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, option)
	}
	for {
		fmt.Fprint(p.out, "Choose a number: ")
		line, err := p.readLine()
		if err != nil {
			return "", err
		}
		n, convErr := strconv.Atoi(line)
		if convErr == nil && n >= 1 && n <= len(options) {
			return options[n-1], nil
		}
		fmt.Fprintf(p.out, "Enter a number between 1 and %d.\n", len(options))
	}
}

func (p *terminalPrompter) Acknowledge(_ context.Context, message string) error {
	fmt.Fprintf(p.out, "%s\nPress Enter when done.", message)
	_, err := p.readLine()
	return err
}

func (p *terminalPrompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

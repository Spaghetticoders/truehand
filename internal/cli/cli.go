// Package cli implements the truehand command line.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

var Version = "0.0.0-dev"

type command struct {
	name    string
	summary string
	run     func(c *ctx, args []string) error
}

type ctx struct {
	stdout, stderr io.Writer
	stdin          io.Reader
	dir            string
}

// exitError carries a specific exit code without printing anything more.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

var commands []command

func register(name, summary string, run func(c *ctx, args []string) error) {
	commands = append(commands, command{name, summary, run})
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "truehand %s — reliable hands for AI coding agents\n\nUsage:\n  truehand <command> [arguments]\n\nCommands:\n", Version)
	cmds := append([]command(nil), commands...)
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].name < cmds[j].name })
	for _, c := range cmds {
		fmt.Fprintf(w, "  %-18s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(w, "\nRun `truehand <command> -h` for the flags of a command.\n")
}

// Run executes the CLI and returns the process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if e2eStart != nil {
		if err := e2eStart(stderr); err != nil {
			fmt.Fprintf(stderr, "truehand: %v\n", err)
			return 2
		}
	}
	dir, _ := os.Getwd()
	return runIn(dir, args, stdin, stdout, stderr)
}

func runIn(dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &ctx{stdout: stdout, stderr: stderr, stdin: stdin, dir: dir}
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, Version)
		return 0
	}
	// Two-word commands ("task new") take precedence over one-word ones.
	var match *command
	var rest []string
	for i := range commands {
		cmd := &commands[i]
		words := strings.Fields(cmd.name)
		if len(args) >= len(words) && strings.Join(args[:len(words)], " ") == cmd.name {
			if match == nil || len(strings.Fields(match.name)) < len(words) {
				match, rest = cmd, args[len(words):]
			}
		}
	}
	if match == nil {
		// "truehand task" or "truehand task --help": list that group's subcommands.
		var group []command
		for _, cmd := range commands {
			if strings.HasPrefix(cmd.name, args[0]+" ") {
				group = append(group, cmd)
			}
		}
		if len(group) > 0 && (len(args) == 1 || args[1] == "-h" || args[1] == "--help" || args[1] == "help") {
			sort.Slice(group, func(i, j int) bool { return group[i].name < group[j].name })
			fmt.Fprintf(stdout, "Usage: truehand %s <command>\n\n", args[0])
			for _, c := range group {
				fmt.Fprintf(stdout, "  %-18s %s\n", c.name, c.summary)
			}
			return 0
		}
		fmt.Fprintf(stderr, "truehand: unknown command %q\n\n", strings.Join(args, " "))
		usage(stderr)
		return 2
	}
	err := match.run(c, rest)
	var ee exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintf(stdout, "Usage: truehand %s\n%s", match.name, strings.TrimPrefix(err.Error(), flag.ErrHelp.Error()+"\n"))
		return 0
	default:
		fmt.Fprintf(stderr, "truehand %s: %v\n", match.name, err)
		return 1
	}
}

// parse parses flags anywhere among positionals; everything after "--" is returned as tail.
func parse(fs *flag.FlagSet, args []string) (pos, tail []string, err error) {
	for i, a := range args {
		if a == "--" {
			tail = args[i+1:]
			args = args[:i]
			break
		}
	}
	fs.SetOutput(io.Discard)
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				var b strings.Builder
				fs.SetOutput(&b)
				fs.PrintDefaults()
				return nil, nil, fmt.Errorf("%w\n%s", flag.ErrHelp, b.String())
			}
			return nil, nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, tail, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

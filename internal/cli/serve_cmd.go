package cli

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/humanauth"
	"github.com/spaghetticoders/truehand/internal/serve"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func init() {
	register("serve", "Serve the Truehand app API for one or more repos (prints the approval token)", cmdServe)
}

func cmdServe(c *ctx, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:7878", "listen address (use 0.0.0.0:7878 to reach it from a phone on your network)")
	web := fs.String("web", "", "directory of the built Truehand web app to serve at /")
	readOnly := fs.Bool("read-only", false, "no approvals from the app (skips the OS authentication)")
	var repos multiFlag
	fs.Var(&repos, "repo", "repo to show (repeatable; default: the current repo)")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	if len(repos) == 0 {
		repos = append(repos, c.dir)
	}
	var roots []string
	for _, r := range repos {
		repo, err := gitx.Open(r)
		if err != nil {
			return fmt.Errorf("%s: %w", r, err)
		}
		roots = append(roots, repo.Root)
	}
	s := &serve.Server{Repos: roots, Token: serve.NewToken(), AnyHost: strings.HasPrefix(*addr, "0.0.0.0") || strings.HasPrefix(*addr, ":")}
	if !*readOnly {
		if core.AgentDetector() != 0 || os.Getenv("TRUEHAND_ACTOR") != "" {
			return errors.New("approvals from the app are enabled by a human: run `truehand serve` in your own terminal, or pass --read-only")
		}
		if err := humanauth.Authenticate("Truehand: allow plan approvals from the Truehand app"); err != nil {
			return fmt.Errorf("%w (use --read-only to serve the board without approvals)", err)
		}
		s.Approvals = true
	}
	if *web != "" {
		if _, err := os.Stat(*web); err != nil {
			return err
		}
		s.Web = os.DirFS(*web)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	defer close(stop)
	go s.Watch(stop, time.Second)
	host := *addr
	if strings.HasPrefix(host, "0.0.0.0") {
		fmt.Fprintln(c.stderr, "warning: listening on all interfaces; anyone on your network with the token can approve plans")
		host = "localhost" + strings.TrimPrefix(host, "0.0.0.0")
	}
	mode := "approvals enabled"
	if !s.Approvals {
		mode = "read-only"
	}
	fmt.Fprintf(c.stdout, "Truehand serving %d repo(s) on http://%s (%s)\nAccess token (keep it to yourself): %s\nOpen: http://%s/?token=%s\n", len(roots), host, mode, s.Token, host, s.Token)
	err = http.Serve(ln, s.Handler())
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

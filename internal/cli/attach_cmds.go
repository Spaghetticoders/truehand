package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/coder/websocket"
	"golang.org/x/term"
)

func init() {
	register("sessions", "List the agent sessions of the Truehand app, on this Mac and on its machines", cmdSessions)
	register("attach", "Open an agent session's terminal here (Ctrl-] detaches, the agent keeps running)", cmdAttach)
}

const detachKey = 0x1d // Ctrl-]

// socketClient talks to the local server over ~/.truehand/app/serve.sock.
func socketClient() (*http.Client, error) {
	p, err := socketPath()
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p)
	}}}, nil
}

type sessionRow struct {
	ID        string   `json:"id"`
	MachineID string   `json:"machineId"`
	ProjectID string   `json:"projectId"`
	TaskID    string   `json:"taskId,omitempty"`
	Role      string   `json:"role"`
	Status    string   `json:"status"`
	Waiting   string   `json:"waitingFor,omitempty"`
	Tail      []string `json:"tail"`
}

type projectRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func fetchSessions() ([]sessionRow, map[string]string, error) {
	client, err := socketClient()
	if err != nil {
		return nil, nil, err
	}
	resp, err := client.Get("http://socket/api/snapshot")
	if err != nil {
		return nil, nil, errors.New("the Truehand app is not running here (no ~/.truehand/app/serve.sock)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("server: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var snap struct {
		Projects []projectRow `json:"projects"`
		Sessions []sessionRow `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, nil, err
	}
	names := map[string]string{}
	for _, p := range snap.Projects {
		names[p.ID] = p.Name
	}
	return snap.Sessions, names, nil
}

func cmdSessions(c *ctx, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	all := fs.Bool("all", false, "include ended sessions")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	list, names, err := fetchSessions()
	if err != nil {
		return err
	}
	var rows []sessionRow
	for _, s := range list {
		if *all || s.Status != "exited" {
			rows = append(rows, s)
		}
	}
	if *asJSON {
		if rows == nil {
			rows = []sessionRow{}
		}
		return printJSON(c.stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.stdout, "no running sessions")
	}
	for _, s := range rows {
		status := s.Status
		if s.Waiting != "" {
			status += ":" + s.Waiting
		}
		last := ""
		if len(s.Tail) > 0 {
			last = s.Tail[len(s.Tail)-1]
		}
		fmt.Fprintf(c.stdout, "%-24s %-8s %-16s %-6s %-6s %-18s %s\n", s.ID, s.MachineID, names[s.ProjectID], s.TaskID, s.Role, status, last)
	}
	return nil
}

// resolveSession accepts a full id or a unique prefix of one (with or without the machine
// prefix).
func resolveSession(ref string) (string, error) {
	list, _, err := fetchSessions()
	if err != nil {
		return "", err
	}
	var match []string
	for _, s := range list {
		_, local, _ := strings.Cut(s.ID, "~")
		if s.ID == ref || local == ref {
			return s.ID, nil
		}
		if strings.HasPrefix(s.ID, ref) || (local != "" && strings.HasPrefix(local, ref)) {
			match = append(match, s.ID)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return "", fmt.Errorf("no session %q: see `truehand sessions`", ref)
	}
	return "", fmt.Errorf("%q matches %d sessions: %s", ref, len(match), strings.Join(match, ", "))
}

func cmdAttach(c *ctx, args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	view := fs.Bool("view", false, "watch only: nothing you type reaches the agent")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: truehand attach <session> [--view]")
	}
	// An agent typing into another agent's terminal would act with that agent's identity.
	if err := notInAgent("attach to a session"); err != nil {
		return err
	}
	sid, err := resolveSession(pos[0])
	if err != nil {
		return err
	}
	client, err := socketClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://socket/api/sessions/"+sid+"/pty", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	in, isTTY := c.stdin.(*os.File)
	isTTY = isTTY && term.IsTerminal(int(in.Fd()))
	if isTTY {
		old, err := term.MakeRaw(int(in.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(in.Fd()), old)
	}
	send := func(v any) {
		b, _ := json.Marshal(v)
		_ = conn.Write(ctx, websocket.MessageText, b)
	}
	resize := func() {
		if !isTTY || *view {
			return
		}
		if w, h, err := term.GetSize(int(in.Fd())); err == nil {
			send(map[string]any{"t": "resize", "c": w, "r": h})
		}
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			resize()
		}
	}()
	resize()

	detached := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := c.stdin.Read(buf)
			if n > 0 {
				b := buf[:n]
				if i := strings.IndexByte(string(b), detachKey); i >= 0 {
					if i > 0 && !*view {
						send(map[string]string{"t": "in", "d": string(b[:i])})
					}
					close(detached)
					return
				}
				if !*view {
					send(map[string]string{"t": "in", "d": string(b)})
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		<-detached
		cancel()
	}()
	status := ""
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if typ == websocket.MessageBinary {
			c.stdout.Write(data)
			continue
		}
		var m struct {
			T      string `json:"t"`
			Status string `json:"status"`
		}
		if json.Unmarshal(data, &m) == nil && m.T == "exit" {
			status = m.Status
		}
	}
	select {
	case <-detached:
		fmt.Fprintf(c.stderr, "\r\n[detached from %s: the agent keeps running]\r\n", sid)
	default:
		if status != "" {
			fmt.Fprintf(c.stderr, "\r\n[session %s %s]\r\n", sid, status)
		}
	}
	return nil
}

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/machines"
)

func init() {
	register("machine add", "Reach another computer over ssh: install truehand in its ~/.truehand and start its peer", cmdMachineAdd)
	register("machine list", "List the machines this Mac reaches, with their status", cmdMachineList)
	register("machine update", "Install this truehand on a machine and restart its peer", cmdMachineUpdate)
	register("machine remove", "Forget a machine (its projects and sessions stay there)", cmdMachineRemove)
}

// sshFor is the system ssh with this user's multiplexed connections; tests replace it.
var sshFor = func() (*machines.SSH, error) {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return nil, errors.New("ssh is not installed")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &machines.SSH{Bin: bin, ControlDir: filepath.Join(home, ".truehand", "ssh")}, nil
}

// binaryFor finds the truehand to install; tests replace it.
var binaryFor = machines.BinaryFor

func notInAgent(what string) error {
	if e2eBuild {
		return nil
	}
	if core.AgentDetector() != 0 || os.Getenv("TRUEHAND_ACTOR") != "" || os.Getenv("TRUEHAND_SESSION") != "" {
		return fmt.Errorf("refusing to %s inside an agent session: run it in your own terminal", what)
	}
	return nil
}

func appStore() (*appstate.Store, error) {
	dir, err := appstate.DefaultDir()
	if err != nil {
		return nil, err
	}
	return appstate.Open(dir)
}

func findMachine(st *appstate.State, ref string) (appstate.Machine, error) {
	for _, m := range st.Machines {
		if m.ID == ref || m.SSHTarget == ref || m.Label == ref {
			return m, nil
		}
	}
	return appstate.Machine{}, fmt.Errorf("no machine %q: see `truehand machine list`", ref)
}

func provision(c *ctx, m appstate.Machine, force bool) (machines.Info, machines.Version, error) {
	ssh, err := sshFor()
	if err != nil {
		return machines.Info{}, machines.Version{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	fmt.Fprintf(c.stderr, "connecting to %s…\n", m.SSHTarget)
	return machines.Provision(ctx, machines.NewPeer(ssh, m.SSHTarget), Version, binaryFor, force)
}

func cmdMachineAdd(c *ctx, args []string) error {
	fs := flag.NewFlagSet("machine add", flag.ContinueOnError)
	label := fs.String("label", "", "name shown in the app (default: the ssh target)")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: truehand machine add <ssh-target> [--label name]")
	}
	if err := notInAgent("add a machine"); err != nil {
		return err
	}
	store, err := appStore()
	if err != nil {
		return err
	}
	m, err := store.AddMachine(appstate.Machine{SSHTarget: pos[0], Label: *label})
	if err != nil {
		return err
	}
	info, v, err := provision(c, m, false)
	if err != nil {
		_ = store.RemoveMachine(m.ID)
		return err
	}
	_ = store.UpdateState(func(st *appstate.State) error {
		if x, ok := st.Machine(m.ID); ok {
			x.OS, x.Arch = info.OS, info.Arch
		}
		return nil
	})
	fmt.Fprintf(c.stdout, "%s %s (%s/%s): truehand %s, claude %v, git %v, gh %v\n", m.ID, m.Label, info.OS, info.Arch, v.Version, info.Tools["claude"], info.Tools["git"], info.Tools["gh"])
	return nil
}

func cmdMachineList(c *ctx, args []string) error {
	fs := flag.NewFlagSet("machine list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	store, err := appStore()
	if err != nil {
		return err
	}
	st, err := store.State()
	if err != nil {
		return err
	}
	ssh, err := sshFor()
	if err != nil {
		return err
	}
	type row struct {
		ID, Label, Target, Status, Version string
	}
	var rows []row
	for _, m := range st.Machines {
		r := row{ID: m.ID, Label: m.Label, Target: m.SSHTarget, Status: "disabled"}
		if !m.Disabled {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			v, err := machines.NewPeer(ssh, m.SSHTarget).Version(ctx)
			cancel()
			switch {
			case errors.Is(err, machines.ErrAuth):
				r.Status = "needs-auth"
			case err != nil:
				r.Status = "offline"
			case v.Version != Version:
				r.Status, r.Version = "needs-update", v.Version
			default:
				r.Status, r.Version = "online", v.Version
			}
		}
		rows = append(rows, r)
	}
	if *asJSON {
		if rows == nil {
			rows = []row{}
		}
		return printJSON(c.stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.stdout, "no machines: add one with `truehand machine add <ssh-target>`")
	}
	for _, r := range rows {
		fmt.Fprintf(c.stdout, "%-12s %-20s %-36s %-12s %s\n", r.ID, r.Label, r.Target, r.Status, r.Version)
	}
	return nil
}

func cmdMachineUpdate(c *ctx, args []string) error {
	fs := flag.NewFlagSet("machine update", flag.ContinueOnError)
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: truehand machine update <id|target|label>")
	}
	if err := notInAgent("update a machine"); err != nil {
		return err
	}
	store, err := appStore()
	if err != nil {
		return err
	}
	st, err := store.State()
	if err != nil {
		return err
	}
	m, err := findMachine(st, pos[0])
	if err != nil {
		return err
	}
	_, v, err := provision(c, m, true)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "%s runs truehand %s\n", m.Label, v.Version)
	return nil
}

func cmdMachineRemove(c *ctx, args []string) error {
	fs := flag.NewFlagSet("machine remove", flag.ContinueOnError)
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: truehand machine remove <id|target|label>")
	}
	store, err := appStore()
	if err != nil {
		return err
	}
	st, err := store.State()
	if err != nil {
		return err
	}
	m, err := findMachine(st, pos[0])
	if err != nil {
		return err
	}
	return store.RemoveMachine(m.ID)
}

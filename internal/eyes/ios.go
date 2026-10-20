package eyes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// IOSSimulator drives an iOS simulator with simctl (lifecycle, screenshots) and idb
// (accessibility tree, taps). idb never moves the Mac's cursor, so the simulator can stay
// behind other windows.
type IOSSimulator struct {
	Device string // UDID, or "" / "booted" for the first booted simulator
	App    string // bundle id; launched only when Launch is set
	Launch bool
	Run    func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (s *IOSSimulator) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, name, args...)
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (s *IOSSimulator) udid(ctx context.Context) (string, error) {
	if s.Device != "" && s.Device != "booted" {
		return s.Device, nil
	}
	out, err := s.run(ctx, "xcrun", "simctl", "list", "devices", "booted", "-j")
	if err != nil {
		return "", err
	}
	var l struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &l); err != nil {
		return "", err
	}
	for _, devs := range l.Devices {
		for _, d := range devs {
			if d.State == "Booted" {
				return d.UDID, nil
			}
		}
	}
	return "", errors.New("no booted iOS simulator: boot one (xcrun simctl boot <udid>) or set eyes.device")
}

type axElement struct {
	Label   string  `json:"AXLabel"`
	Value   *string `json:"AXValue"`
	Type    string  `json:"type"`
	Enabled bool    `json:"enabled"`
	Frame   struct {
		X, Y, Width, Height float64
	} `json:"frame"`
}

func (s *IOSSimulator) tree(ctx context.Context, udid string) ([]axElement, error) {
	out, err := s.run(ctx, "idb", "ui", "describe-all", "--udid", udid, "--json")
	if err != nil {
		return nil, fmt.Errorf("%w (install idb: brew install idb-companion && pip install fb-idb)", err)
	}
	var els []axElement
	if err := json.Unmarshal(bytes.TrimSpace(out), &els); err != nil {
		return nil, fmt.Errorf("idb describe-all: %w", err)
	}
	return els, nil
}

// stableTree re-reads the tree until two reads agree: a tree read while a list decelerates
// sends the tap to the wrong row.
func (s *IOSSimulator) stableTree(ctx context.Context, udid string) ([]axElement, error) {
	prev, err := s.tree(ctx, udid)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 5; i++ {
		time.Sleep(250 * time.Millisecond)
		cur, err := s.tree(ctx, udid)
		if err != nil {
			return nil, err
		}
		a, _ := json.Marshal(prev)
		b, _ := json.Marshal(cur)
		if bytes.Equal(a, b) {
			return cur, nil
		}
		prev = cur
	}
	return prev, nil
}

func find(els []axElement, label string) *axElement {
	for i := range els {
		e := &els[i]
		if e.Type == "Application" || e.Frame.Width <= 0 || e.Frame.Height <= 0 {
			continue
		}
		if e.Label == label || strings.EqualFold(strings.TrimSpace(e.Label), strings.TrimSpace(label)) {
			return e
		}
	}
	return nil
}

func (s *IOSSimulator) Capture(ctx context.Context, route []Step) (*Capture, error) {
	udid, err := s.udid(ctx)
	if err != nil {
		return nil, err
	}
	if s.Launch {
		if s.App == "" {
			return nil, errors.New("eyes.app (bundle id) is required to launch the app")
		}
		if _, err := s.run(ctx, "xcrun", "simctl", "launch", "--terminate-running-process", udid, s.App); err != nil {
			return nil, err
		}
		time.Sleep(2 * time.Second)
	}
	for i, st := range route {
		if err := s.step(ctx, udid, st); err != nil {
			return nil, fmt.Errorf("route step %d: %w", i+1, err)
		}
	}
	dir, err := os.MkdirTemp("", "truehand-eyes-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	shot := filepath.Join(dir, "screen.png")
	if _, err := s.run(ctx, "xcrun", "simctl", "io", udid, "screenshot", "--type=png", shot); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(shot)
	if err != nil {
		return nil, err
	}
	els, err := s.stableTree(ctx, udid)
	if err != nil {
		return nil, err
	}
	c := &Capture{PNG: b, Device: udid}
	var appW float64
	for _, e := range els {
		if e.Type == "Application" {
			appW = e.Frame.Width
			continue
		}
		if t := strings.TrimSpace(e.Label); t != "" {
			c.Texts = append(c.Texts, t)
		}
		if e.Value != nil && strings.TrimSpace(*e.Value) != "" {
			c.Texts = append(c.Texts, strings.TrimSpace(*e.Value))
		}
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(b)); err == nil && appW > 0 {
		c.Scale = float64(cfg.Width) / appW
	}
	return c, nil
}

func (s *IOSSimulator) step(ctx context.Context, udid string, st Step) error {
	switch {
	case st.Tap != "":
		var target *axElement
		err := waitFor(ctx, 10*time.Second, func() (bool, error) {
			els, err := s.stableTree(ctx, udid)
			if err != nil {
				return false, err
			}
			target = find(els, st.Tap)
			return target != nil, nil
		})
		if err != nil {
			return fmt.Errorf("tap %q: element not found: %w", st.Tap, err)
		}
		x := target.Frame.X + target.Frame.Width/2
		y := target.Frame.Y + target.Frame.Height/2
		_, err = s.run(ctx, "idb", "ui", "tap", "--udid", udid, fmt.Sprintf("%.0f", x), fmt.Sprintf("%.0f", y))
		time.Sleep(600 * time.Millisecond)
		return err
	case st.Wait != "":
		return waitFor(ctx, 15*time.Second, func() (bool, error) {
			els, err := s.tree(ctx, udid)
			return err == nil && find(els, st.Wait) != nil, err
		})
	case st.Sleep != "":
		d, err := time.ParseDuration(st.Sleep)
		if err != nil {
			return err
		}
		time.Sleep(d)
		return nil
	case st.Swipe != "":
		// 0.5s is a drag of a few rows; 0.1s would be a fling to the end of the list.
		from, to := "600", "300"
		if st.Swipe == "down" {
			from, to = "300", "600"
		}
		_, err := s.run(ctx, "idb", "ui", "swipe", "--udid", udid, "--duration", "0.5", "200", from, "200", to)
		time.Sleep(800 * time.Millisecond)
		return err
	case st.Text != "":
		// idb sends US key codes: on other keyboard layouts punctuation arrives garbled.
		for _, r := range st.Text {
			if !(r == ' ' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return fmt.Errorf("text %q: only letters, digits and spaces can be typed reliably", st.Text)
			}
		}
		_, err := s.run(ctx, "idb", "ui", "text", "--udid", udid, st.Text)
		return err
	}
	return errors.New("empty or unsupported step for ios-simulator")
}

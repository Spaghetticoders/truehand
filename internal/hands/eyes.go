package hands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"sort"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/eyes"
	"github.com/spaghetticoders/truehand/internal/figma"
	"github.com/spaghetticoders/truehand/internal/imgdiff"
)

type EyesOptions struct {
	Screenshot string // use this PNG instead of capturing (file adapter)
	Launch     bool
	// Adapter and Figma are injectable for tests.
	Adapter eyes.Adapter
	Figma   *figma.Client
}

type EyesReport struct {
	Screen       string         `json:"screen"`
	Node         string         `json:"node"`
	FileKey      string         `json:"file_key"`
	Device       string         `json:"device"`
	ScalePxPerPt float64        `json:"scale_px_per_pt"`
	DesignScale  float64        `json:"design_scale_px_per_pt"`
	Size         [2]int         `json:"size_px"`
	DesignSizePt [2]float64     `json:"design_size_pt"`
	Ratio        float64        `json:"diff_ratio"`
	MaxRatio     float64        `json:"max_diff_ratio"`
	HeightDelta  int            `json:"height_mismatch_px"`
	Regions      []imgdiff.Rect `json:"diff_regions_pt"`
	MissingTexts []string       `json:"missing_texts"`
	CheckedTexts int            `json:"checked_texts"`
	ScreenTexts  []string       `json:"screen_texts,omitempty"`
	Masks        []config.Rect  `json:"masks_pt"`
}

const defaultMaxRatio = 0.03

var defaultMask = []config.Rect{{X: 0, Y: 0, W: 10000, H: 54}}

// Eyes captures a screen of the running app and compares it with its Figma node: pixels
// (masked, after scaling the render to the device) and copy (every visible Figma text must
// be on screen).
func Eyes(ctx context.Context, ws *core.Workspace, t *board.Task, screen string, o EyesOptions) (*Result, error) {
	cfg := ws.Config.Eyes
	sc, ok := cfg.Screens[screen]
	if !ok {
		var names []string
		for n := range cfg.Screens {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown screen %q: add it under eyes.screens (configured: %s)", screen, strings.Join(names, ", "))
	}
	if sc.Node == "" {
		return nil, fmt.Errorf("eyes.screens.%s.node is required", screen)
	}
	fileKey := sc.FileKey
	if fileKey == "" {
		fileKey = cfg.Figma.FileKey
	}
	if fileKey == "" {
		return nil, errors.New("no Figma file: set eyes.figma.file_key or eyes.screens.<name>.file_key")
	}
	fc := o.Figma
	if fc == nil {
		env := cfg.Figma.TokenEnv
		if env == "" {
			env = "FIGMA_TOKEN"
		}
		tok := os.Getenv(env)
		if tok == "" {
			return nil, fmt.Errorf("%s is not set: export a Figma personal access token", env)
		}
		fc = &figma.Client{Token: tok}
		if err := fc.Me(ctx); err != nil {
			return nil, err
		}
	}
	source := ws.Config.EyesAdapter()
	if o.Screenshot != "" && source != "file" {
		return nil, fmt.Errorf("--screenshot needs eyes.adapter: file; this project captures the screen with %s", source)
	}
	adapter := o.Adapter
	if adapter == nil {
		var err error
		if adapter, err = adapterFor(cfg, o); err != nil {
			return nil, err
		}
	}

	s, err := Begin(ws, t, "eyes", screen)
	if err != nil {
		return nil, err
	}
	node, err := fc.Node(ctx, fileKey, sc.Node)
	if err != nil {
		return nil, err
	}
	if node.Box.Width <= 0 {
		return nil, fmt.Errorf("figma node %s has no size", sc.Node)
	}
	route := make([]eyes.Step, len(sc.Route))
	for i, st := range sc.Route {
		route[i] = eyes.Step(st)
	}
	capt, err := adapter.Capture(ctx, route)
	if err != nil {
		return nil, err
	}
	actual, err := png.Decode(bytes.NewReader(capt.PNG))
	if err != nil {
		return nil, fmt.Errorf("screenshot is not a PNG: %w", err)
	}
	w := actual.Bounds().Dx()
	scale := float64(w) / node.Box.Width
	renderScale := min(max(scale, 0.01), 4)
	designPNG, err := fc.Render(ctx, fileKey, sc.Node, renderScale)
	if err != nil {
		return nil, err
	}
	design, err := png.Decode(bytes.NewReader(designPNG))
	if err != nil {
		return nil, fmt.Errorf("figma render is not a PNG: %w", err)
	}

	masks := cfg.Mask
	if masks == nil {
		masks = defaultMask
	}
	masks = append(append([]config.Rect(nil), masks...), sc.Mask...)
	// Masks are in device points; the design may be drawn for a narrower device.
	devScale := capt.Scale
	if devScale <= 0 {
		devScale = scale
	}
	var pxMasks []imgdiff.Rect
	for _, m := range masks {
		pxMasks = append(pxMasks, imgdiff.Rect{X: int(m.X * devScale), Y: int(m.Y * devScale), W: int(m.W*devScale + 0.5), H: int(m.H*devScale + 0.5)})
	}
	diff := imgdiff.Compare(actual, design, imgdiff.Options{Threshold: cfg.Threshold, Masks: pxMasks, Cell: int(8 * scale)})

	maxRatio := sc.MaxDeltaRatio
	if maxRatio == 0 {
		maxRatio = cfg.MaxDeltaRatio
	}
	if maxRatio == 0 {
		maxRatio = defaultMaxRatio
	}
	rep := EyesReport{
		Screen: screen, Node: figma.NormalizeID(sc.Node), FileKey: fileKey, Device: capt.Device,
		ScalePxPerPt: devScale, DesignScale: scale, Size: [2]int{w, actual.Bounds().Dy()}, DesignSizePt: [2]float64{node.Box.Width, node.Box.Height},
		Ratio: diff.Ratio, MaxRatio: maxRatio, HeightDelta: diff.HeightMismatch, Masks: masks,
		MissingTexts: []string{}, ScreenTexts: capt.Texts,
	}
	for _, r := range diff.Regions {
		rep.Regions = append(rep.Regions, imgdiff.Rect{X: int(float64(r.X) / devScale), Y: int(float64(r.Y) / devScale), W: int(float64(r.W)/devScale + 0.5), H: int(float64(r.H)/devScale + 0.5)})
	}
	textsChecked := !sc.SkipTexts && len(capt.Texts) > 0
	if textsChecked {
		rep.MissingTexts, rep.CheckedTexts = missingTexts(node.Texts, capt.Texts, masks, sc.IgnoreTexts)
	}

	if err := s.WriteFile("actual.png", capt.PNG); err != nil {
		return nil, err
	}
	if err := s.WriteFile("design.png", designPNG); err != nil {
		return nil, err
	}
	if err := writePNG(s.File("diff.png"), diff.Diff); err != nil {
		return nil, err
	}
	rb, _ := json.MarshalIndent(rep, "", "  ")
	if err := s.WriteFile("report.json", rb); err != nil {
		return nil, err
	}
	s.Rec.Data = map[string]any{"diff_ratio": diff.Ratio, "max_diff_ratio": maxRatio, "missing_texts": rep.MissingTexts, "node": rep.Node, "source": source, "texts_checked": textsChecked}

	var problems []string
	if diff.Ratio > maxRatio {
		where := ""
		if len(rep.Regions) > 0 {
			var rs []string
			for _, r := range rep.Regions[:min(3, len(rep.Regions))] {
				rs = append(rs, fmt.Sprintf("(%d,%d %dx%d)", r.X, r.Y, r.W, r.H))
			}
			where = ", largest regions in pt: " + strings.Join(rs, " ")
		}
		problems = append(problems, fmt.Sprintf("%.1f%% of pixels differ (max %.1f%%)%s", diff.Ratio*100, maxRatio*100, where))
	}
	// A design taller than the screen is normal for scrolling pages: noted, not failed.
	note := ""
	if diff.HeightMismatch != 0 {
		note = fmt.Sprintf("; note: design is %dpx %s than the screen (compared the overlapping part)", abs(diff.HeightMismatch), map[bool]string{true: "taller", false: "shorter"}[diff.HeightMismatch > 0])
	}
	if source != "file" && !sc.SkipTexts && len(capt.Texts) == 0 {
		problems = append(problems, "no texts on screen to check (empty accessibility tree?); set skip_texts for screens drawn on a canvas")
	}
	if len(rep.MissingTexts) > 0 {
		problems = append(problems, fmt.Sprintf("%d Figma text(s) not on screen: %s", len(rep.MissingTexts), quoteList(rep.MissingTexts, 5)))
	}
	textNote := fmt.Sprintf("%d text(s) checked", rep.CheckedTexts)
	if !textsChecked {
		textNote = "texts not checked"
	}
	if len(problems) == 0 {
		return s.Finish(evidence.Pass, fmt.Sprintf("%s matches Figma %s: %.2f%% pixels differ (max %.1f%%), %s%s", screen, rep.Node, diff.Ratio*100, maxRatio*100, textNote, note), "")
	}
	return s.Finish(evidence.Fail, screen+": "+strings.Join(problems, "; ")+note, "open diff.png next to design.png and actual.png; fix the screen, or change the design with the designer — never silently")
}

func adapterFor(cfg config.Eyes, o EyesOptions) (eyes.Adapter, error) {
	if o.Screenshot != "" {
		return &eyes.File{Path: o.Screenshot}, nil
	}
	switch cfg.Adapter {
	case "ios-simulator", "":
		return &eyes.IOSSimulator{Device: cfg.Device, App: cfg.App, Launch: cfg.Launch || o.Launch}, nil
	case "file":
		return nil, errors.New("eyes.adapter is file: pass --screenshot <png>")
	}
	return nil, fmt.Errorf("unknown eyes adapter %q (known: ios-simulator, file)", cfg.Adapter)
}

// missingTexts returns Figma texts that do not appear on screen. Screen labels often join
// several layers ("Name\nEmail"), so matching is by normalised substring.
func missingTexts(layers []figma.TextLayer, screen []string, masks []config.Rect, ignore []string) ([]string, int) {
	hay := normalize(strings.Join(screen, "\n"))
	ign := map[string]bool{}
	for _, s := range ignore {
		ign[normalize(s)] = true
	}
	missing := []string{}
	checked := 0
	seen := map[string]bool{}
	for _, l := range layers {
		n := normalize(l.Text)
		if n == "" || ign[n] || seen[n] || inMask(l.Box, masks) {
			continue
		}
		seen[n] = true
		checked++
		if !strings.Contains(hay, n) {
			missing = append(missing, strings.TrimSpace(l.Text))
		}
	}
	return missing, checked
}

func inMask(b figma.Box, masks []config.Rect) bool {
	cx, cy := b.X+b.Width/2, b.Y+b.Height/2
	for _, m := range masks {
		if cx >= m.X && cx < m.X+m.W && cy >= m.Y && cy < m.Y+m.H {
			return true
		}
	}
	return false
}

func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("’", "'", "‘", "'", "“", "\"", "”", "\"", " ", " ", "…", "...").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func quoteList(s []string, n int) string {
	var q []string
	for _, x := range s[:min(n, len(s))] {
		q = append(q, fmt.Sprintf("%q", x))
	}
	if len(s) > n {
		q = append(q, fmt.Sprintf("and %d more", len(s)-n))
	}
	return strings.Join(q, ", ")
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

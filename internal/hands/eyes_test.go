package hands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/eyes"
	"github.com/spaghetticoders/truehand/internal/figma"
)

func pngOf(w, h int, paint func(img *image.NRGBA)) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	if paint != nil {
		paint(img)
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func box(img *image.NRGBA, x0, y0, x1, y1 int, c color.Color) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.Set(x, y, c)
		}
	}
}

type fakeAdapter struct {
	png   []byte
	texts []string
}

func (f fakeAdapter) Capture(context.Context, []eyes.Step) (*eyes.Capture, error) {
	return &eyes.Capture{PNG: f.png, Texts: f.texts, Device: "fake"}, nil
}

// fakeFigma serves a 100x200pt frame containing texts and a design rendered at 3x.
func fakeFigma(t *testing.T, design []byte) *figma.Client {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/files/"):
			fmt.Fprint(w, `{"nodes":{"1:2":{"document":{"id":"1:2","name":"Login","type":"FRAME","absoluteBoundingBox":{"x":1000,"y":0,"width":100,"height":200},"children":[
			  {"type":"TEXT","characters":"9:41","absoluteBoundingBox":{"x":1005,"y":5,"width":20,"height":10}},
			  {"type":"TEXT","characters":"Accedi","absoluteBoundingBox":{"x":1010,"y":100,"width":40,"height":10}},
			  {"type":"TEXT","characters":"Hidden","visible":false,"absoluteBoundingBox":{"x":1010,"y":120,"width":40,"height":10}},
			  {"type":"TEXT","characters":"Password dimenticata?","absoluteBoundingBox":{"x":1010,"y":150,"width":80,"height":10}}]}}}}`)
		case strings.HasPrefix(r.URL.Path, "/v1/images/"):
			if r.URL.Query().Get("scale") != "3" {
				t.Errorf("render scale %s, want 3", r.URL.Query().Get("scale"))
			}
			json.NewEncoder(w).Encode(map[string]any{"images": map[string]string{"1:2": srv.URL + "/render.png"}})
		case r.URL.Path == "/render.png":
			w.Write(design)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &figma.Client{Token: "x", BaseURL: srv.URL}
}

func eyesWorkspace(t *testing.T) *core.Workspace {
	dir := t.TempDir()
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "a@b.c"}, {"commit", "-q", "--allow-empty", "-m", "chore: x"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	os.MkdirAll(filepath.Join(dir, ".truehand"), 0o755)
	os.WriteFile(filepath.Join(dir, ".truehand/config.yaml"), []byte(`version: 0
eyes:
  figma: { file_key: KEY }
  screens:
    login: { node: "1-2" }
gates: {}
`), 0o644)
	ws, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestEyesPass(t *testing.T) {
	ws := eyesWorkspace(t)
	tk, _ := ws.NewTask("T-1", "x", "")
	design := pngOf(300, 600, func(img *image.NRGBA) { box(img, 30, 300, 150, 330, color.Black) })
	// Same screen, but a different status bar (masked by default: top 54pt = 162px).
	actual := pngOf(300, 600, func(img *image.NRGBA) {
		box(img, 30, 300, 150, 330, color.Black)
		box(img, 0, 0, 300, 40, color.NRGBA{0, 0, 255, 255})
	})
	res, err := Eyes(context.Background(), ws, tk, "login", EyesOptions{
		Adapter: fakeAdapter{actual, []string{"Accedi", "Password dimenticata?"}}, Figma: fakeFigma(t, design),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "pass" {
		t.Fatalf("expected pass, got %s: %s", res.Verdict, res.Summary)
	}
	if len(res.Evidence) != 4 {
		t.Fatalf("expected actual/design/diff/report, got %+v", res.Evidence)
	}
}

func TestEyesFailsOnPixelsAndCopy(t *testing.T) {
	ws := eyesWorkspace(t)
	tk, _ := ws.NewTask("T-1", "x", "")
	design := pngOf(300, 600, func(img *image.NRGBA) { box(img, 30, 300, 270, 400, color.Black) })
	actual := pngOf(300, 600, nil)
	res, err := Eyes(context.Background(), ws, tk, "login", EyesOptions{
		Adapter: fakeAdapter{actual, []string{"Accedi", "Password dimenticata"}}, Figma: fakeFigma(t, design),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "fail" {
		t.Fatalf("expected fail, got %s", res.Verdict)
	}
	for _, want := range []string{"of pixels differ", "\"Password dimenticata?\""} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("summary misses %q: %s", want, res.Summary)
		}
	}
	if strings.Contains(res.Summary, "9:41") || strings.Contains(res.Summary, "Hidden") {
		t.Errorf("masked or hidden texts must not be required: %s", res.Summary)
	}
}

func TestEyesUnknownScreen(t *testing.T) {
	ws := eyesWorkspace(t)
	tk, _ := ws.NewTask("T-1", "x", "")
	_, err := Eyes(context.Background(), ws, tk, "nope", EyesOptions{Adapter: fakeAdapter{}, Figma: &figma.Client{}})
	if err == nil || !strings.Contains(err.Error(), "configured: login") {
		t.Fatalf("expected helpful error, got %v", err)
	}
}

func TestEyesScreenshotOnlyWithFileAdapter(t *testing.T) {
	ws := eyesWorkspace(t)
	tk, _ := ws.NewTask("T-1", "x", "")
	shot := filepath.Join(t.TempDir(), "s.png")
	os.WriteFile(shot, pngOf(300, 600, nil), 0o644)
	_, err := Eyes(context.Background(), ws, tk, "login", EyesOptions{Screenshot: shot, Figma: fakeFigma(t, pngOf(300, 600, nil))})
	if err == nil || !strings.Contains(err.Error(), "eyes.adapter: file") {
		t.Fatalf("--screenshot on a simulator project must be refused, got %v", err)
	}
}

func TestEyesGateRejectsOtherSource(t *testing.T) {
	ws := eyesWorkspace(t)
	cfgPath := filepath.Join(ws.Repo.Root, ".truehand/config.yaml")
	os.WriteFile(cfgPath, []byte("version: 0\neyes:\n  adapter: file\n  figma: { file_key: KEY }\n  screens:\n    login: { node: \"1-2\" }\ngates:\n  verified:\n    - check: eyes\n"), 0o644)
	ws, _ = core.Open(ws.Repo.Root)
	tk, _ := ws.NewTask("T-1", "x", "")
	design := pngOf(300, 600, nil)
	shot := filepath.Join(t.TempDir(), "s.png")
	os.WriteFile(shot, design, 0o644)
	res, err := Eyes(context.Background(), ws, tk, "login", EyesOptions{Screenshot: shot, Figma: fakeFigma(t, design)})
	if err != nil || res.Verdict != "pass" {
		t.Fatalf("file adapter run: %v %+v", err, res)
	}
	if r, _ := ws.Check(tk, "verified"); !r.Pass {
		t.Fatalf("file evidence counts when the adapter is file: %+v", r)
	}
	os.WriteFile(cfgPath, []byte("version: 0\neyes:\n  figma: { file_key: KEY }\n  screens:\n    login: { node: \"1-2\" }\ngates:\n  verified:\n    - check: eyes\n"), 0o644)
	ws, _ = core.Open(ws.Repo.Root)
	r, _ := ws.Check(tk, "verified")
	if r.Pass || !strings.Contains(r.Items[0].Message, "capture the real screen") {
		t.Fatalf("an image from file must not satisfy a simulator project: %+v", r)
	}
}

func TestEyesSimulatorWithoutTextsFails(t *testing.T) {
	ws := eyesWorkspace(t)
	tk, _ := ws.NewTask("T-1", "x", "")
	img := pngOf(300, 600, nil)
	res, err := Eyes(context.Background(), ws, tk, "login", EyesOptions{Adapter: fakeAdapter{img, nil}, Figma: fakeFigma(t, img)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "fail" || !strings.Contains(res.Summary, "no texts on screen") {
		t.Fatalf("a simulator capture with no texts must not pass: %s %s", res.Verdict, res.Summary)
	}
}

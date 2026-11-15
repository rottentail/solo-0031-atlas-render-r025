package raster

import (
	"atlas-render/internal/model"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// edgeDemoScene stresses the requested cases together: a rotated rectangle,
// a rotated translucent ellipse nested inside a rotated clip group, and an
// overlapping translucent veil on a light opaque background.
func edgeDemoScene() model.Snapshot {
	return model.Snapshot{Document: model.Document{Width: 120, Height: 120, Background: "#F4F0E6", Layers: []model.Layer{
		{ID: "plate", Kind: "rectangle", Width: 80, Height: 46,
			Transform: model.Transform{X: 18, Y: 24, ScaleX: 1, ScaleY: 1, Degrees: 32},
			Opacity:   1, Fill: model.Paint{Kind: "solid", Color: "#1A5276"}},
		{ID: "clipped", Kind: "group",
			Transform: model.Transform{X: 40, Y: 44, ScaleX: 1, ScaleY: 1, Degrees: 18},
			Opacity:   1, Clip: &model.Rect{Width: 52, Height: 44},
			Children: []model.Layer{{
				ID: "disc", Kind: "ellipse", Width: 60, Height: 60,
				Transform: model.Transform{X: -6, Y: -10, ScaleX: 1, ScaleY: 1, Degrees: -22},
				Opacity:   .7, Fill: model.Paint{Kind: "solid", Color: "#C0392B"}}}},
		{ID: "veil", Kind: "polygon", Width: 90, Height: 60,
			Transform: model.Transform{X: 24, Y: 50, ScaleX: 1, ScaleY: 1, Degrees: -12},
			Opacity:   .45, Fill: model.Paint{Kind: "solid", Color: "#7D3C98"},
			Points: []model.Point{{X: 0, Y: 0}, {X: 90, Y: 12}, {X: 78, Y: 60}, {X: 6, Y: 46}}},
	}}}
}

func luminance(c [4]uint8) float64 {
	return 0.299*float64(c[0]) + 0.587*float64(c[1]) + 0.114*float64(c[2])
}

// sourceColors are the exact flat colors the demo layers paint; a pixel that
// matches one of them is fully covered rather than an edge blend.
var sourceColors = map[[3]uint8]bool{
	{26, 82, 118}:  true, // #1A5276 plate
	{192, 57, 43}:  true, // #C0392B disc
	{125, 60, 152}: true, // #7D3C98 veil
}

func isBackground(c [3]uint8) bool { return c == [3]uint8{244, 240, 230} }

// edgeReport measures observable aliasing signals from the luminance field:
//   - hardAt[t]: adjacent pixel pairs whose luminance jump exceeds t. Binary
//     coverage concentrates a whole edge transition into one maximum jump;
//     coverage ramps split it into smaller jumps.
//   - maxStep: the single largest adjacent luminance jump (a hard 0->source
//     staircase reaches the full source/background contrast).
//   - partial: background-adjacent pixels that are neither flat background
//     nor an exact source color — the antialiased edge population.
func edgeReport(img *image.NRGBA) (hardAt map[float64]int, maxStep float64, partial int) {
	bounds := img.Bounds()
	h, w := bounds.Dy(), bounds.Dx()
	lum := make([][]float64, h)
	rgb := make([][][3]uint8, h)
	for y := 0; y < h; y++ {
		lum[y] = make([]float64, w)
		rgb[y] = make([][3]uint8, w)
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(bounds.Min.X+x, bounds.Min.Y+y)
			rgb[y][x] = [3]uint8{c.R, c.G, c.B}
			lum[y][x] = luminance([4]uint8{c.R, c.G, c.B, c.A})
		}
	}
	hardAt = map[float64]int{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			jump := func(a, b float64) float64 {
				d := a - b
				if d < 0 {
					d = -d
				}
				return d
			}
			if x+1 < w {
				if d := jump(lum[y][x], lum[y][x+1]); d > maxStep {
					maxStep = d
				}
			}
			if y+1 < h {
				if d := jump(lum[y][x], lum[y+1][x]); d > maxStep {
					maxStep = d
				}
			}
			for _, t := range []float64{40, 80, 120, 160} {
				if (x+1 < w && jump(lum[y][x], lum[y][x+1]) > t) ||
					(y+1 < h && jump(lum[y][x], lum[y+1][x]) > t) {
					hardAt[t]++
				}
			}
			if isBackground(rgb[y][x]) {
				continue
			}
			bgNeighbor := (x > 0 && isBackground(rgb[y][x-1])) ||
				(x+1 < w && isBackground(rgb[y][x+1])) ||
				(y > 0 && isBackground(rgb[y-1][x])) ||
				(y+1 < h && isBackground(rgb[y+1][x]))
			if bgNeighbor && !sourceColors[rgb[y][x]] {
				partial++
			}
		}
	}
	return hardAt, maxStep, partial
}

// TestEdgeQualityReport renders the demo in both modes at scale 2 and reports
// the reduction in hard staircase steps. When EDGE_OUT is set it also writes
// the two PNGs plus a side-by-side composite for visual inspection.
func TestEdgeQualityReport(t *testing.T) {
	scene := edgeDemoScene()
	legacy, e := CompileWithRendering(scene, 2, model.RenderLegacy)
	if e != nil {
		t.Fatal(e)
	}
	aa, e := CompileWithRendering(scene, 2, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	legacyImg, e := legacy.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	aaImg, e := aa.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	lHard, lMax, lPartial := edgeReport(legacyImg)
	aHard, aMax, aPartial := edgeReport(aaImg)
	t.Logf("EDGE QUALITY (scale 2, %dx%d output):", aaImg.Bounds().Dx(), aaImg.Bounds().Dy())
	t.Logf("  legacy:    max luminance jump=%.0f  partial-edge pixels=%d", lMax, lPartial)
	t.Logf("  antialias: max luminance jump=%.0f  partial-edge pixels=%d", aMax, aPartial)
	for _, threshold := range []float64{40, 80, 120, 160} {
		reduction := 100 * float64(lHard[threshold]-aHard[threshold]) / float64(lHard[threshold])
		t.Logf("  jumps >%.0f: legacy=%d antialias=%d  (%.1f%% fewer)", threshold, lHard[threshold], aHard[threshold], reduction)
	}
	if aMax >= lMax {
		t.Fatalf("antialiasing did not soften the maximum edge jump: %.0f -> %.0f", lMax, aMax)
	}
	if aPartial <= lPartial {
		t.Fatalf("expected a populated coverage ramp, got partial pixels %d -> %d", lPartial, aPartial)
	}
	if aHard[120] >= lHard[120] {
		t.Fatalf("hard staircase steps (>120) were not reduced: %d -> %d", lHard[120], aHard[120])
	}
	if out := os.Getenv("EDGE_OUT"); out != "" {
		if e = os.MkdirAll(out, 0755); e != nil {
			t.Fatal(e)
		}
		writePNG := func(name string, img *image.NRGBA) {
			f, err := os.Create(filepath.Join(out, name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err = png.Encode(f, img); err != nil {
				t.Fatal(err)
			}
		}
		writePNG("edge-legacy.png", legacyImg)
		writePNG("edge-antialias.png", aaImg)
	}
}

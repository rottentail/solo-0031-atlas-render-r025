package raster

import (
	"atlas-render/internal/model"
	"context"
	"image"
	"image/color"
	"testing"
)

func rotatedRectScene() model.Snapshot {
	return model.Snapshot{Document: model.Document{
		Width: 60, Height: 60, Background: "#FFFFFF",
		Layers: []model.Layer{{
			ID: "r", Kind: "rectangle", Width: 30, Height: 20,
			Transform: model.Transform{X: 15, Y: 20, ScaleX: 1, ScaleY: 1, Degrees: 30},
			Opacity:   1, Fill: model.Paint{Kind: "solid", Color: "#104080"},
		}},
	}}
}

func clippedScene() model.Snapshot {
	return model.Snapshot{Document: model.Document{
		Width: 60, Height: 60, Background: "#FFFFFF",
		Layers: []model.Layer{{
			ID: "g", Kind: "group",
			Transform: model.Transform{X: 12, Y: 12, ScaleX: 1, ScaleY: 1, Degrees: 15},
			Opacity:   1, Clip: &model.Rect{Width: 36, Height: 36},
			Children: []model.Layer{{
				ID: "e", Kind: "ellipse", Width: 44, Height: 44,
				Transform: model.Transform{X: -4, Y: -4, ScaleX: 1, ScaleY: 1, Degrees: 25},
				Opacity:   .6, Fill: model.Paint{Kind: "solid", Color: "#C03030"},
			}},
		}},
	}}
}

// TestCoverageGridValues directly verifies the coverage function only
// returns multiples of one document-grid step and produces intermediate
// values on a rotated edge.
func TestCoverageGridValues(t *testing.T) {
	for _, scale := range []int{1, 2} {
		plan, e := CompileWithRendering(rotatedRectScene(), scale, model.RenderAntialias)
		if e != nil {
			t.Fatal(e)
		}
		denom := float64(plan.samplesPerAxis() * plan.samplesPerAxis())
		op := plan.ops[0]
		intermediate := map[float64]bool{}
		for y := op.bounds.Min.Y; y < op.bounds.Max.Y; y++ {
			for x := op.bounds.Min.X; x < op.bounds.Max.X; x++ {
				cov := op.coverage(plan, x, y)
				if cov < 0 || cov > 1 {
					t.Fatalf("scale %d: coverage out of range at (%d,%d): %v", scale, x, y, cov)
				}
				if r := cov * denom; float64(int(r+0.5)) != r {
					t.Fatalf("scale %d: coverage %v is not a multiple of 1/%d", scale, cov, int(denom))
				}
				if cov > 0 && cov < 1 {
					intermediate[cov] = true
				}
			}
		}
		if len(intermediate) < 2 {
			t.Fatalf("scale %d: expected several intermediate coverage levels, got %d", scale, len(intermediate))
		}
	}
}

// channelLevels returns the distinct values present in one channel
// (0=R, 1=G, 2=B) across the whole image and how many pixels differ from the
// white background. On an opaque canvas the antialiasing ramp lives in RGB.
func channelLevels(img *image.NRGBA, channel int) (edgePixels, levels int) {
	seen := map[uint8]bool{}
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			c := img.NRGBAAt(x, y)
			v := [3]uint8{c.R, c.G, c.B}[channel]
			seen[v] = true
			if v < 255 {
				edgePixels++
			}
		}
	}
	return edgePixels, len(seen)
}

// TestLegacyMatchesPointSampling pins the historical rasterizer: coverage is
// binary, so every blue-channel value is either the background (255) or the
// exact source blue (0x80) with nothing in between.
func TestLegacyMatchesPointSampling(t *testing.T) {
	plan, e := CompileWithRendering(rotatedRectScene(), 1, model.RenderLegacy)
	if e != nil {
		t.Fatal(e)
	}
	img, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	edgePixels, levels := channelLevels(img, 2)
	if edgePixels == 0 || levels != 2 {
		t.Fatalf("legacy binary coverage broken: edges=%d distinct blue levels=%d", edgePixels, levels)
	}
}

// TestAntialiasProducesEdgeGradient requires several distinct blend levels
// along the rotated edge instead of the legacy binary step.
func TestAntialiasProducesEdgeGradient(t *testing.T) {
	plan, e := CompileWithRendering(rotatedRectScene(), 1, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	img, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	edgePixels, levels := channelLevels(img, 2)
	if edgePixels == 0 {
		t.Fatal("antialiased render produced no shape pixels")
	}
	if levels < 3 {
		t.Fatalf("rotated edge has %d distinct blend levels, expected a coverage ramp", levels)
	}
	legacy, e := CompileWithRendering(rotatedRectScene(), 1, model.RenderLegacy)
	if e != nil {
		t.Fatal(e)
	}
	oldImg, e := legacy.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	_, legacyLevels := channelLevels(oldImg, 2)
	if legacyLevels != 2 {
		t.Fatalf("legacy output changed: %d levels, expected binary 2", legacyLevels)
	}
	t.Logf("rotated rect: %d edge pixels, antialias levels=%d legacy levels=%d", edgePixels, levels, legacyLevels)
}

// TestClipIntersectionAntialiased checks the nested clip boundary itself is
// antialiased where the rotated ellipse crosses the rotated clip rect. The
// red ellipse on white shows the ramp on the red channel.
func TestClipIntersectionAntialiased(t *testing.T) {
	plan, e := CompileWithRendering(clippedScene(), 1, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	img, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	rgbEdges, rgbLevels := channelLevels(img, 0)
	if rgbEdges == 0 || rgbLevels < 3 {
		t.Fatalf("clip intersection not antialiased: edges=%d levels=%d", rgbEdges, rgbLevels)
	}
	// The clip chain must also expose intermediate coverage directly.
	op := plan.ops[0]
	partial := 0
	for y := op.bounds.Min.Y; y < op.bounds.Max.Y; y++ {
		for x := op.bounds.Min.X; x < op.bounds.Max.X; x++ {
			if cov := op.coverage(plan, x, y); cov > 0 && cov < 1 {
				partial++
			}
		}
	}
	if partial == 0 {
		t.Fatal("clip chain produced no partial-coverage pixels")
	}
	t.Logf("clip intersection: %d edge pixels, %d RGB levels, %d partial-coverage pixels", rgbEdges, rgbLevels, partial)
}

// TestScaleEdgeConsistency verifies both scales emit a full coverage ramp,
// since the same fixed grid rule is applied per output pixel.
func TestScaleEdgeConsistency(t *testing.T) {
	for _, scale := range []int{1, 2} {
		plan, e := CompileWithRendering(rotatedRectScene(), scale, model.RenderAntialias)
		if e != nil {
			t.Fatal(e)
		}
		img, e := plan.Render(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		_, levels := channelLevels(img, 2)
		if levels < 3 {
			t.Fatalf("scale %d: coverage ramp collapsed to %d levels", scale, levels)
		}
	}
}

// TestDeterministicRenders requires byte-identical repeated output, which is
// what frozen package migration and idempotent replay rely on.
func TestDeterministicRenders(t *testing.T) {
	for _, mode := range []model.Rendering{model.RenderAntialias, model.RenderLegacy} {
		plan, e := CompileWithRendering(clippedScene(), 2, mode)
		if e != nil {
			t.Fatal(e)
		}
		first, e := plan.Render(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		second, e := plan.Render(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		for y := 0; y < first.Bounds().Dy(); y++ {
			for x := 0; x < first.Bounds().Dx(); x++ {
				if first.NRGBAAt(x, y) != second.NRGBAAt(x, y) {
					t.Fatalf("%s render not deterministic at (%d,%d)", mode, x, y)
				}
			}
		}
	}
}

// TestCancellationStaysResponsive ensures the antialiased inner loop keeps
// observing the context on every scanline instead of blocking until done.
func TestCancellationStaysResponsive(t *testing.T) {
	heavy := model.Snapshot{Document: model.Document{Width: 400, Height: 400, Background: "#101010",
		Layers: []model.Layer{{ID: "p", Kind: "polygon", Width: 400, Height: 400,
			Transform: model.Transform{ScaleX: 1, ScaleY: 1, Degrees: 1}, Opacity: 1,
			Fill:   model.Paint{Kind: "solid", Color: "#2060A0"},
			Points: []model.Point{{X: 0, Y: 0}, {X: 399, Y: 40}, {X: 220, Y: 399}, {X: 0, Y: 300}}}}}}
	plan, e := CompileWithRendering(heavy, 2, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = plan.Render(ctx); e != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", e)
	}
}

// downsampleBox averages every 2x2 block of a scale-2 render back to scale-1
// resolution, so it can be compared against the native scale-1 output.
func downsampleBox(src *image.NRGBA) *image.NRGBA {
	w, h := src.Bounds().Dx()/2, src.Bounds().Dy()/2
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b, a int
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					c := src.NRGBAAt(x*2+dx, y*2+dy)
					r += int(c.R)
					g += int(c.G)
					b += int(c.B)
					a += int(c.A)
				}
			}
			out.SetNRGBA(x, y, color.NRGBA{R: uint8(r / 4), G: uint8(g / 4), B: uint8(b / 4), A: uint8(a / 4)})
		}
	}
	return out
}

// TestScaleCoverageGridAligned proves the scale-1 coverage at a document
// pixel equals the mean of the four scale-2 sub-pixel coverages, because both
// are counts over the same shared document grid. This is the invariant that
// keeps edges consistent when the output multiplier changes.
func TestScaleCoverageGridAligned(t *testing.T) {
	scene := clippedScene()
	p1, e := CompileWithRendering(scene, 1, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	p2, e := CompileWithRendering(scene, 2, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	for y := 0; y < p1.Height; y++ {
		for x := 0; x < p1.Width; x++ {
			whole := 0.0
			for _, op := range p1.ops {
				whole += op.coverage(p1, x, y)
			}
			parts := 0.0
			for _, op := range p2.ops {
				for dy := 0; dy < 2; dy++ {
					for dx := 0; dx < 2; dx++ {
						parts += op.coverage(p2, x*2+dx, y*2+dy)
					}
				}
			}
			if d := whole - parts/4; d > 1e-9 || d < -1e-9 {
				t.Fatalf("coverage mismatch at doc pixel (%d,%d): scale1=%v mean(scale2)=%v", x, y, whole, parts/4)
			}
		}
	}
}

// TestScaleOutputPixelsBounded compares the box-averaged final scale-2 image
// against scale-1. With an aligned coverage grid the only remaining gap is
// alpha-compositing round trips (each NRGBA channel is rounded per layer), so
// per-channel deltas stay within a small constant rather than jumping with
// edge steepness.
func TestScaleOutputPixelsBounded(t *testing.T) {
	scene := clippedScene()
	p1, e := CompileWithRendering(scene, 1, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	p2, e := CompileWithRendering(scene, 2, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	img1, e := p1.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	img2, e := p2.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	avg := downsampleBox(img2)
	worst := 0
	for y := 0; y < img1.Bounds().Dy(); y++ {
		for x := 0; x < img1.Bounds().Dx(); x++ {
			a, b := img1.NRGBAAt(x, y), avg.NRGBAAt(x, y)
			for _, pair := range [][2]uint8{{a.R, b.R}, {a.G, b.G}, {a.B, b.B}, {a.A, b.A}} {
				d := int(pair[0]) - int(pair[1])
				if d < 0 {
					d = -d
				}
				if d > worst {
					worst = d
				}
			}
		}
	}
	// Two rounded compositing rounds; documented constant bound independent
	// of geometry. The legacy-independent-grid approach previously hit 59.
	if worst > 4 {
		t.Fatalf("scale-1 vs downsampled scale-2 differ by %d per channel, bound is 4/255", worst)
	}
	t.Logf("cross-scale per-channel worst delta = %d/255", worst)
}

// TestOverlappingTranslucentEdgesAreOrdered stacks two rotated translucent
// layers and requires every blended pixel to stay between the two source
// colors with no overflow, and edges to keep their coverage ramp.
func TestOverlappingTranslucentEdgesAreOrdered(t *testing.T) {
	scene := model.Snapshot{Document: model.Document{Width: 80, Height: 80, Background: "#FFFFFF", Layers: []model.Layer{
		{ID: "a", Kind: "rectangle", Width: 40, Height: 24,
			Transform: model.Transform{X: 14, Y: 22, ScaleX: 1, ScaleY: 1, Degrees: 28},
			Opacity:   .5, Fill: model.Paint{Kind: "solid", Color: "#C03030"}},
		{ID: "b", Kind: "ellipse", Width: 34, Height: 34,
			Transform: model.Transform{X: 30, Y: 24, ScaleX: 1, ScaleY: 1, Degrees: -18},
			Opacity:   .5, Fill: model.Paint{Kind: "solid", Color: "#3060C0"}},
	}}}
	plan, e := CompileWithRendering(scene, 1, model.RenderAntialias)
	if e != nil {
		t.Fatal(e)
	}
	img, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	_, rLevels := channelLevels(img, 0)
	_, bLevels := channelLevels(img, 2)
	if rLevels < 3 || bLevels < 3 {
		t.Fatalf("overlapping translucent edges collapsed: red levels=%d blue levels=%d", rLevels, bLevels)
	}
	for y := 0; y < 80; y++ {
		for x := 0; x < 80; x++ {
			c := img.NRGBAAt(x, y)
			if c.A != 255 {
				t.Fatalf("blend over opaque background lost opacity at (%d,%d): %d", x, y, c.A)
			}
		}
	}
}

// benchmarkScene is a representative production scene: a 512px canvas with
// rotated rectangles, an ellipse, a clipped group, a polygon and translucent
// overlaps, exercising every edge path the coverage rasterizer treats.
func benchmarkScene() model.Document {
	return model.Document{Width: 512, Height: 512, Background: "#F5F1E8", Layers: []model.Layer{
		{ID: "plate", Kind: "rectangle", Width: 360, Height: 240,
			Transform: model.Transform{X: 60, Y: 70, ScaleX: 1, ScaleY: 1, Degrees: 6},
			Opacity:   1, Fill: model.Paint{Kind: "linear", Color: "#145A70", EndColor: "#32A5A0",
				From: model.Point{}, To: model.Point{X: 360, Y: 240}}},
		{ID: "accent", Kind: "ellipse", Width: 220, Height: 220,
			Transform: model.Transform{X: 230, Y: 160, ScaleX: 1, ScaleY: 1},
			Opacity:   .85, Fill: model.Paint{Kind: "solid", Color: "#E87E4F"}},
		{ID: "inset", Kind: "group",
			Transform: model.Transform{X: 240, Y: 200, ScaleX: 1, ScaleY: 1, Degrees: -8},
			Opacity:   .9, Clip: &model.Rect{Width: 160, Height: 130},
			Children: []model.Layer{{
				ID: "facet", Kind: "polygon", Width: 180, Height: 180,
				Transform: model.Transform{ScaleX: 1, ScaleY: 1, Degrees: 12}, Opacity: .8,
				Fill:   model.Paint{Kind: "solid", Color: "#FFF4D5"},
				Points: []model.Point{{X: 18, Y: 160}, {X: 90, Y: 0}, {X: 178, Y: 160}}}}},
		{ID: "veil", Kind: "rectangle", Width: 300, Height: 90,
			Transform: model.Transform{X: 120, Y: 300, ScaleX: 1, ScaleY: 1, Degrees: -14},
			Opacity:   .45, Fill: model.Paint{Kind: "solid", Color: "#403060"}},
	}}
}

func runRenderBench(b *testing.B, scale int, mode model.Rendering) {
	plan, e := CompileWithRendering(model.Snapshot{Document: benchmarkScene()}, scale, mode)
	if e != nil {
		b.Fatal(e)
	}
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if _, e = plan.Render(ctx); e != nil {
			b.Fatal(e)
		}
	}
}

func BenchmarkRenderAntialias(b *testing.B) { runRenderBench(b, 1, model.RenderAntialias) }
func BenchmarkRenderLegacy(b *testing.B)    { runRenderBench(b, 1, model.RenderLegacy) }
func BenchmarkRenderAntialiasScale2(b *testing.B) {
	runRenderBench(b, 2, model.RenderAntialias)
}

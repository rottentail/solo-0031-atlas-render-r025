package raster

import (
	"atlas-render/internal/geometry"
	"atlas-render/internal/model"
	"atlas-render/internal/paint"
	"context"
	"image"
	"image/color"
	"image/draw"
	"math"
)

// documentGrid is the number of coverage samples per document pixel along one
// axis. At scale 2 each output pixel covers half a document pixel and uses
// documentGrid/2 samples per axis; at scale 1 it uses the full grid. Sample
// positions always coincide with the same fixed grid inside document
// pixels, so box-averaging four scale-2 pixels reproduces the scale-1
// coverage exactly and edges stay consistent across output multipliers.
const documentGrid = model.DocumentCoverageGrid

// samplePoints returns, for output pixel (x,y), sub-pixel offsets whose
// world-space positions are centroids of cells in one shared document grid.
// The document grid splits every document pixel into documentGrid cells per
// axis. A scale-1 output pixel spans all grid cells of one document pixel; a
// scale-2 pixel spans the (documentGrid/2)x(documentGrid/2) cells of one
// half. Both therefore reference the identical world-space points, so
// box-averaging four scale-2 pixels reproduces scale-1 coverage exactly.
func (p Plan) samplePoints(x, y int) [][2]float64 {
	sub := documentGrid / p.Scale
	// Document grid column/row at the start of this output pixel.
	col0 := (x/p.Scale)*documentGrid + (x%p.Scale)*sub
	row0 := (y/p.Scale)*documentGrid + (y%p.Scale)*sub
	pts := make([][2]float64, 0, sub*sub)
	for gy := 0; gy < sub; gy++ {
		for gx := 0; gx < sub; gx++ {
			// World position of the cell centroid, converted back to an
			// offset inside the output pixel in output-pixel units.
			wx := (float64(col0+gx) + .5) / float64(documentGrid)
			wy := (float64(row0+gy) + .5) / float64(documentGrid)
			pts = append(pts, [2]float64{
				wx*float64(p.Scale) - float64(x),
				wy*float64(p.Scale) - float64(y),
			})
		}
	}
	return pts
}

func (p Plan) samplesPerAxis() int { return documentGrid / p.Scale }

func (p Plan) Render(ctx context.Context) (*image.NRGBA, error) {
	img := image.NewNRGBA(image.Rect(0, 0, p.Width, p.Height))
	bg, _ := paint.Parse(p.Background)
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	for _, op := range p.ops {
		if op.opacity == 0 {
			continue
		}
		if p.Rendering == model.RenderLegacy {
			if e := renderLegacy(ctx, p, op, img); e != nil {
				return nil, e
			}
			continue
		}
		if e := renderAntialias(ctx, p, op, img); e != nil {
			return nil, e
		}
	}
	return img, nil
}

// worldAt maps an output pixel plus a sub-pixel offset back to world
// coordinates (document units).
func (p Plan) worldAt(x, y int, ox, oy float64) model.Point {
	inv := 1 / float64(p.Scale)
	return model.Point{X: (float64(x) + ox) * inv, Y: (float64(y) + oy) * inv}
}

// contained applies the layer shape rule and every inherited clip at one
// world-space sample point.
func (op operation) contained(world model.Point) bool {
	local := op.inverse.Apply(world)
	l := op.layer
	if !geometry.InRect(local, model.Rect{Width: l.Width, Height: l.Height}) {
		return false
	}
	for _, c := range op.clips {
		if !geometry.InRect(c.inverse.Apply(world), c.rect) {
			return false
		}
	}
	switch l.Kind {
	case "ellipse":
		dx, dy := 2*local.X/l.Width-1, 2*local.Y/l.Height-1
		if dx*dx+dy*dy > 1 {
			return false
		}
	case "polygon":
		if !geometry.InPolygon(local, l.Points) {
			return false
		}
	}
	return true
}

// fullyInside reports whether every corner of an output pixel is guaranteed
// to be covered. Pixel corners are conservative: a positive result means all
// interior grid samples are covered without evaluating them. Polygons can be
// concave and never take this shortcut.
func (op operation) fullyInside(p Plan, x, y int) bool {
	// Compute the four pixel-corner world points once; every layer and clip
	// test reuses them instead of re-running the inverse matrix.
	corners := [4][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}}
	worlds := [4]model.Point{}
	for i, q := range corners {
		worlds[i] = p.worldAt(x, y, q[0], q[1])
	}
	l := op.layer
	loX, loY := math.Inf(1), math.Inf(1)
	hiX, hiY := math.Inf(-1), math.Inf(-1)
	for _, w := range worlds {
		local := op.inverse.Apply(w)
		loX = math.Min(loX, local.X)
		hiX = math.Max(hiX, local.X)
		loY = math.Min(loY, local.Y)
		hiY = math.Max(hiY, local.Y)
	}
	if l.Kind == "rectangle" {
		if loX < 0 || loY < 0 || hiX >= l.Width || hiY >= l.Height {
			return false
		}
	} else {
		// Non-rectangular layer: its local bounds rect must contain the
		// pixel first, then the convex shape test runs per corner.
		if loX < 0 || loY < 0 || hiX > l.Width || hiY > l.Height {
			return false
		}
		for _, w := range worlds {
			local := op.inverse.Apply(w)
			if l.Kind == "ellipse" {
				dx, dy := 2*local.X/l.Width-1, 2*local.Y/l.Height-1
				if dx*dx+dy*dy > 1 {
					return false
				}
			} else {
				return false
			}
		}
	}
	// Every clip rect must contain the pixel entirely. Clips are axis-aligned
	// rectangles in their own local space; bound the four mapped corners.
	for _, c := range op.clips {
		cx0, cy0 := math.Inf(1), math.Inf(1)
		cx1, cy1 := math.Inf(-1), math.Inf(-1)
		for _, w := range worlds {
			pt := c.inverse.Apply(w)
			cx0 = math.Min(cx0, pt.X)
			cy0 = math.Min(cy0, pt.Y)
			cx1 = math.Max(cx1, pt.X)
			cy1 = math.Max(cy1, pt.Y)
		}
		if cx0 < c.rect.X || cy0 < c.rect.Y || cx1 >= c.rect.X+c.rect.Width || cy1 >= c.rect.Y+c.rect.Height {
			return false
		}
	}
	return true
}

// fullyOutside reports whether every corner of an output pixel lies outside
// at least one required region, in which case no interior grid sample can be
// covered. The same conservative corner bounding used by fullyInside applies.
func (op operation) fullyOutside(p Plan, x, y int) bool {
	corners := [4][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}}
	worlds := [4]model.Point{}
	for i, q := range corners {
		worlds[i] = p.worldAt(x, y, q[0], q[1])
	}
	l := op.layer
	loX, loY := math.Inf(1), math.Inf(1)
	hiX, hiY := math.Inf(-1), math.Inf(-1)
	for _, w := range worlds {
		local := op.inverse.Apply(w)
		loX = math.Min(loX, local.X)
		hiX = math.Max(hiX, local.X)
		loY = math.Min(loY, local.Y)
		hiY = math.Max(hiY, local.Y)
	}
	// Pixel box misses the layer's local bounds rectangle entirely.
	if hiX <= 0 || hiY <= 0 || loX >= l.Width || loY >= l.Height {
		return true
	}
	// Pixel box misses any clip rectangle entirely.
	for _, c := range op.clips {
		cx0, cy0 := math.Inf(1), math.Inf(1)
		cx1, cy1 := math.Inf(-1), math.Inf(-1)
		for _, w := range worlds {
			pt := c.inverse.Apply(w)
			cx0 = math.Min(cx0, pt.X)
			cy0 = math.Min(cy0, pt.Y)
			cx1 = math.Max(cx1, pt.X)
			cy1 = math.Max(cy1, pt.Y)
		}
		if cx1 <= c.rect.X || cy1 <= c.rect.Y || cx0 >= c.rect.X+c.rect.Width || cy0 >= c.rect.Y+c.rect.Height {
			return true
		}
	}
	return false
}

// coverage returns the fraction (0..1) of the output pixel covered by the
// layer shape and its whole clip chain. Every output pixel evaluates the full
// document-grid subset; there is no center-sample shortcut, otherwise a
// scale-1 pixel could return zero while its four scale-2 sub-pixels found
// coverage, breaking cross-scale edge consistency.
func (op operation) coverage(p Plan, x, y int) float64 {
	// Convex layers whose whole output pixel lies inside every clip are
	// provably fully covered at every grid sample. The check is conservative
	// (pixel corners) and scale independent, so it cannot break the
	// cross-scale alignment of partial pixels.
	if op.layer.Kind != "polygon" && op.fullyInside(p, x, y) {
		return 1
	}
	if op.fullyOutside(p, x, y) {
		return 0
	}
	offsets := p.samplePoints(x, y)
	hit := 0
	for _, s := range offsets {
		if op.contained(p.worldAt(x, y, s[0], s[1])) {
			hit++
		}
	}
	return float64(hit) / float64(len(offsets))
}

func (op operation) sampleColor(world model.Point) color.NRGBA {
	local := op.inverse.Apply(world)
	l := op.layer
	if op.bitmap != nil {
		b := op.bitmap.Bounds()
		sx := b.Min.X + int(local.X/l.Width*float64(b.Dx()))
		sy := b.Min.Y + int(local.Y/l.Height*float64(b.Dy()))
		return color.NRGBAModel.Convert(op.bitmap.At(sx, sy)).(color.NRGBA)
	}
	return paint.Sample(l.Fill, local)
}

func renderAntialias(ctx context.Context, p Plan, op operation, img *image.NRGBA) error {
	for y := op.bounds.Min.Y; y < op.bounds.Max.Y; y++ {
		if e := ctx.Err(); e != nil {
			return e
		}
		for x := op.bounds.Min.X; x < op.bounds.Max.X; x++ {
			cov := op.coverage(p, x, y)
			if cov == 0 {
				continue
			}
			// Color is evaluated at the pixel center, the same sample point
			// the legacy rasterizer uses, so fills and gradients keep their
			// historical color semantics while only the edge gains coverage.
			c := op.sampleColor(p.worldAt(x, y, .5, .5))
			c.A = uint8(math.Round(float64(c.A) * op.opacity * cov))
			blend(img, x, y, c)
		}
	}
	return nil
}

func renderLegacy(ctx context.Context, p Plan, op operation, img *image.NRGBA) error {
	for y := op.bounds.Min.Y; y < op.bounds.Max.Y; y++ {
		if e := ctx.Err(); e != nil {
			return e
		}
		for x := op.bounds.Min.X; x < op.bounds.Max.X; x++ {
			world := p.worldAt(x, y, .5, .5)
			if !op.contained(world) {
				continue
			}
			c := op.sampleColor(world)
			c.A = uint8(math.Round(float64(c.A) * op.opacity))
			blend(img, x, y, c)
		}
	}
	return nil
}

func blend(img *image.NRGBA, x, y int, s color.NRGBA) {
	d := img.NRGBAAt(x, y)
	sa, da := float64(s.A)/255, float64(d.A)/255
	a := sa + da*(1-sa)
	if a == 0 {
		img.SetNRGBA(x, y, color.NRGBA{})
		return
	}
	mix := func(u, v uint8) uint8 { return uint8(math.Round((float64(u)*sa + float64(v)*da*(1-sa)) / a)) }
	img.SetNRGBA(x, y, color.NRGBA{R: mix(s.R, d.R), G: mix(s.G, d.G), B: mix(s.B, d.B), A: uint8(math.Round(a * 255))})
}

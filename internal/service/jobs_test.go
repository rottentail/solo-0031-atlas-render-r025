package service

import (
	"atlas-render/internal/model"
	"atlas-render/internal/persist"
	"atlas-render/internal/raster"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, e := persist.Open(filepath.Join(t.TempDir(), "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Service{Store: store}
}

func snapshotDoc() model.Document {
	return model.Document{Width: 40, Height: 40, Background: "#FFFFFF", Layers: []model.Layer{{
		ID: "r", Kind: "rectangle", Width: 20, Height: 12,
		Transform: model.Transform{X: 8, Y: 10, ScaleX: 1, ScaleY: 1, Degrees: 22},
		Opacity:   1, Fill: model.Paint{Kind: "solid", Color: "#2050A0"},
	}}}
}

func freezeSnapshot(t *testing.T, s *Service, id string) model.Snapshot {
	t.Helper()
	doc := snapshotDoc()
	if e := s.Store.Update(func(st *model.State) error {
		now := time.Now().UTC()
		st.Scenes[id] = model.Scene{ID: id, Name: "edge", Revision: 1, State: "active", Document: doc, UpdatedAt: now}
		snap := model.Snapshot{ID: "snap-" + id, SceneID: id, Revision: 1, State: "active", Document: doc, CreatedAt: now}
		snap.Digest = model.SnapshotDigest(snap)
		st.Snapshots[snap.ID] = snap
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return s.Store.Read().Snapshots["snap-"+id]
}

// TestSubmitRenderingDefaultsAndReplay checks the default selector, exact
// replay under the same idempotency key, and 409 when the same key changes
// semantics (mode or scale).
func TestSubmitRenderingDefaultsAndReplay(t *testing.T) {
	s := newTestService(t)
	freezeSnapshot(t, s, "scene1")

	first, e := s.Submit("snap-scene1", 1, "", "key-1")
	if e != nil {
		t.Fatal(e)
	}
	if first.Rendering != model.RenderAntialias {
		t.Fatalf("default rendering = %q, want antialias", first.Rendering)
	}
	replay, e := s.Submit("snap-scene1", 1, "antialias", "key-1")
	if e != nil || replay.ID != first.ID {
		t.Fatalf("same-key replay must return the original job: %+v err=%v", replay, e)
	}
	if _, e = s.Submit("snap-scene1", 1, "legacy", "key-1"); e == nil {
		t.Fatal("same key with different rendering must conflict")
	}
	if _, e = s.Submit("snap-scene1", 2, "antialias", "key-1"); e == nil {
		t.Fatal("same key with different scale must conflict")
	}
	legacy, e := s.Submit("snap-scene1", 1, "legacy", "key-2")
	if e != nil || legacy.Rendering != model.RenderLegacy {
		t.Fatalf("legacy selector not persisted: %+v err=%v", legacy, e)
	}
	if _, e = s.Submit("snap-scene1", 1, "sketchy", "key-3"); e == nil {
		t.Fatal("unknown rendering value must be rejected")
	}
}

// TestRenderingSurvivesStoreRoundTrip ensures the selector is part of durable
// job state, so a frozen package migrated onto another instance still
// reproduces the chosen output semantics.
func TestRenderingSurvivesStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store, e := persist.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s := &Service{Store: store}
	freezeSnapshot(t, s, "scene2")
	job, e := s.Submit("snap-scene2", 2, "legacy", "keep")
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := persist.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	got := reopened.Read().Jobs[job.ID]
	if got.Rendering != model.RenderLegacy || got.Scale != 2 {
		t.Fatalf("reloaded job lost rendering semantics: %+v", got)
	}
}

// TestBudgetGateStillEnforced proves the antialiased path never bypasses the
// pre-enqueue compile and work-budget admission.
func TestBudgetGateStillEnforced(t *testing.T) {
	s := newTestService(t)
	doc := model.Document{Width: 1536, Height: 1536, Background: "#111111", Layers: []model.Layer{}}
	for i := 0; i < 12; i++ {
		doc.Layers = append(doc.Layers, model.Layer{
			ID: "l", Kind: "rectangle", Width: 1536, Height: 1536,
			Transform: model.Transform{ScaleX: 1, ScaleY: 1}, Opacity: 1,
			Fill: model.Paint{Kind: "solid", Color: "#202020"},
		})
		doc.Layers[i].ID = "l" + string(rune('a'+i))
	}
	if e := s.Store.Update(func(st *model.State) error {
		now := time.Now().UTC()
		st.Scenes["big"] = model.Scene{ID: "big", Name: "big", Revision: 1, State: "active", Document: doc, UpdatedAt: now}
		snap := model.Snapshot{ID: "snap-big", SceneID: "big", Revision: 1, State: "active", Document: doc, CreatedAt: now}
		snap.Digest = model.SnapshotDigest(snap)
		st.Snapshots[snap.ID] = snap
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Submit("snap-big", 2, "antialias", "over-budget"); e == nil {
		t.Fatal("over-budget scene must be rejected at enqueue for antialiased jobs")
	}
	if _, e := s.Submit("snap-big", 2, "legacy", "over-budget-legacy"); e == nil {
		t.Fatal("over-budget scene must be rejected at enqueue for legacy jobs")
	}
}

// TestFrozenPackageReproducesSelection exports a frozen snapshot, imports it
// into a fresh service, and re-renders with the same selector. Pixel output
// must be identical on both sides, proving a migrated frozen package still
// reproduces the chosen rendering semantics.
func TestFrozenPackageReproducesSelection(t *testing.T) {
	src := newTestService(t)
	freezeSnapshot(t, src, "scene9")
	pkg, e := src.Export("snap-scene9")
	if e != nil {
		t.Fatal(e)
	}
	dst := newTestService(t)
	if _, e = dst.Import("imported", "migrated", pkg); e != nil {
		t.Fatal(e)
	}
	// The imported scene needs a fresh snapshot of the identical document.
	imp := dst.Store.Read().Scenes["imported"]
	if e = dst.Store.Update(func(st *model.State) error {
		snap := model.Snapshot{ID: "snap-imported", SceneID: "imported", Revision: 1, State: "active", Document: imp.Document, CreatedAt: imp.UpdatedAt}
		snap.Digest = model.SnapshotDigest(snap)
		st.Snapshots[snap.ID] = snap
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []model.Rendering{model.RenderAntialias, model.RenderLegacy} {
		if _, e := src.Submit("snap-scene9", 1, mode, "src-"+string(mode)); e != nil {
			t.Fatal(e)
		}
		j2, e := dst.Submit("snap-imported", 1, mode, "dst-"+string(mode))
		if e != nil || j2.Rendering != mode {
			t.Fatalf("migrated job lost selector %s: %+v err=%v", mode, j2, e)
		}
	}
	// Deterministic rasterization means the same document and selector yield
	// identical pixels regardless of which service produced them.
	for _, mode := range []model.Rendering{model.RenderAntialias, model.RenderLegacy} {
		planA, e := raster.CompileWithRendering(src.Store.Read().Snapshots["snap-scene9"], 1, mode)
		if e != nil {
			t.Fatal(e)
		}
		planB, e := raster.CompileWithRendering(dst.Store.Read().Snapshots["snap-imported"], 1, mode)
		if e != nil {
			t.Fatal(e)
		}
		imgA, e := planA.Render(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		imgB, e := planB.Render(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if model.CanonicalDigest(imgA.Pix) != model.CanonicalDigest(imgB.Pix) {
			t.Fatalf("migrated frozen package rendered different pixels for selector %s", mode)
		}
	}
}

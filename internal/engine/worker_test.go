package engine

import (
	"atlas-render/internal/model"
	"atlas-render/internal/persist"
	"atlas-render/internal/service"
	"context"
	"path/filepath"
	"testing"
	"time"
)

// heavyScene mirrors the cancel-render walkthrough: sixteen full-canvas
// layers at scale 1 stay just inside the 40M layer-pixel budget while taking
// long enough that cancellation must interrupt a running render.
func heavyState(t *testing.T) (model.Document, model.Snapshot) {
	doc := model.Document{Width: 1500, Height: 1500, Background: "#101010", Layers: []model.Layer{}}
	for i := 0; i < 16; i++ {
		doc.Layers = append(doc.Layers, model.Layer{
			ID: "sheet", Kind: "rectangle", Width: 1500, Height: 1500,
			Transform: model.Transform{ScaleX: 1, ScaleY: 1, Degrees: float64(i%7) - 3},
			Opacity:   .9, Fill: model.Paint{Kind: "solid", Color: "#2060A0"},
		})
		doc.Layers[i].ID = "sheet-" + string(rune('a'+i))
	}
	snap := model.Snapshot{ID: "snap-heavy", SceneID: "scene-heavy", Revision: 1, State: "active", Document: doc}
	snap.Digest = model.SnapshotDigest(snap)
	return doc, snap
}

// TestCancellationLatency runs the real worker and measures how long a
// committed cancel takes to land on an in-flight antialiased render. The
// per-scanline context check must keep this well under a second instead of
// waiting for the whole frame.
func TestCancellationLatency(t *testing.T) {
	dir := t.TempDir()
	store, e := persist.Open(filepath.Join(dir, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	doc, snap := heavyState(t)
	now := time.Now().UTC()
	if e = store.Update(func(st *model.State) error {
		st.Scenes["scene-heavy"] = model.Scene{ID: "scene-heavy", Name: "heavy", Revision: 1, State: "active", Document: doc, UpdatedAt: now}
		st.Snapshots[snap.ID] = snap
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	svc := &service.Service{Store: store}
	job, e := svc.Submit(snap.ID, 1, "antialias", "heavy-cancel")
	if e != nil {
		t.Fatal(e)
	}
	worker := New(store)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go worker.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if store.Read().Jobs[job.ID].State == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if store.Read().Jobs[job.ID].State != "running" {
		t.Fatal("job never reached running")
	}
	start := time.Now()
	if _, e = svc.Cancel(job.ID); e != nil {
		t.Fatal(e)
	}
	worker.Cancel(job.ID)
	for time.Now().Before(deadline) {
		if store.Read().Jobs[job.ID].State == "cancelled" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(start)
	if got := store.Read().Jobs[job.ID].State; got != "cancelled" {
		t.Fatalf("job state = %s, want cancelled", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation took %v, exceeds 500ms responsiveness bound", elapsed)
	}
	t.Logf("committed cancel -> cancelled state in %v", elapsed)
}

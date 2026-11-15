package persist

import (
	"atlas-render/internal/model"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLegacyStateFileMigrates writes a state file in the pre-selector format
// (a queued job with no rendering field) and requires Open to accept it,
// defaulting the job to antialiased output without losing any other fact.
func TestLegacyStateFileMigrates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	doc := model.Document{Width: 8, Height: 8, Background: "#FFFFFF", Layers: []model.Layer{{
		ID: "r", Kind: "rectangle", Width: 4, Height: 4,
		Transform: model.Transform{X: 1, Y: 1, ScaleX: 1, ScaleY: 1},
		Opacity:   1, Fill: model.Paint{Kind: "solid", Color: "#102030"},
	}}}
	snap := model.Snapshot{ID: "snap-1", SceneID: "sc-1", Revision: 1, State: "active", Document: doc}
	snap.Digest = model.SnapshotDigest(snap)
	st := model.NewState()
	st.Scenes["sc-1"] = model.Scene{ID: "sc-1", Name: "n", Revision: 1, State: "active", Document: doc}
	st.Snapshots["snap-1"] = snap
	st.Jobs["job-1"] = model.Job{ID: "job-1", SnapshotID: "snap-1", State: "queued", Scale: 2, Attempt: 1, Payload: snap}
	raw, e := json.Marshal(st)
	if e != nil {
		t.Fatal(e)
	}
	// Remove the rendering key (omitted anyway at zero value) to emulate the
	// exact on-disk shape written by an older binary.
	var generic map[string]any
	if e = json.Unmarshal(raw, &generic); e != nil {
		t.Fatal(e)
	}
	delete(generic["jobs"].(map[string]any)["job-1"].(map[string]any), "rendering")
	raw, e = json.Marshal(generic)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := Open(path)
	if e != nil {
		t.Fatalf("legacy state file must migrate: %v", e)
	}
	defer store.Close()
	job := store.Read().Jobs["job-1"]
	if job.Rendering != model.RenderAntialias {
		t.Fatalf("migrated job rendering = %q, want antialias default", job.Rendering)
	}
	if job.Scale != 2 || job.Attempt != 1 || job.State != "queued" {
		t.Fatalf("migration altered job facts: %+v", job)
	}
}

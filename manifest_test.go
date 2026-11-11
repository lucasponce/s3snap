package s3snap

import (
	"testing"
	"time"
)

func TestManifestRoundTrip(t *testing.T) {
	in := &Manifest{
		SnapshotID:     "t1",
		TakenAt:        time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC),
		RegistryID:     "reg",
		SourceBucket:   "src",
		SnapshotBucket: "snap",
		ObjectCount:    1,
		Objects: []ObjectMeta{
			{Key: "reg/docker/blob/data", VersionID: "v7", Size: 42, ETag: `"abc"`},
		},
	}

	data, err := encodeManifest(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}

	if out.SnapshotID != in.SnapshotID || out.ObjectCount != 1 || len(out.Objects) != 1 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
	if out.Objects[0] != in.Objects[0] {
		t.Errorf("object mismatch: got %+v want %+v", out.Objects[0], in.Objects[0])
	}
	if !out.TakenAt.Equal(in.TakenAt) {
		t.Errorf("time mismatch: got %v want %v", out.TakenAt, in.TakenAt)
	}
}

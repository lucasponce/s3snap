package s3snap_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lucasponce/s3snap"
	"github.com/lucasponce/s3snap/memstore"
)

const (
	sourceBucket = "registry-src"
	snapBucket   = "registry-snap"
	registryID   = "reg0"
)

// newFixture returns a Snapshotter wired to a fresh in-memory store, plus the
// store so tests can inspect/seed it directly.
func newFixture(t *testing.T) (*s3snap.Snapshotter, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	s, err := s3snap.New(store, s3snap.Config{
		SourceBucket:   sourceBucket,
		SnapshotBucket: snapBucket,
		RegistryID:     registryID,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, store
}

// deployProject simulates deploy-test-app.sh: it writes into the source bucket the
// objects a built image for <project> would push to the registry — a shared base
// blob (deduplicated across projects), a project-unique blob, and the per-project
// repository link that identifies the project in the bucket.
func deployProject(t *testing.T, store *memstore.Store, project string) {
	t.Helper()
	ctx := context.Background()
	objs := map[string][]byte{
		"docker/registry/v2/blobs/sha256/ba/base/data":                                           []byte("shared-base-layer"),
		fmt.Sprintf("docker/registry/v2/blobs/sha256/%s/data", project):                          []byte("unique-" + project),
		fmt.Sprintf("docker/registry/v2/repositories/%s/app/_manifests/revisions/link", project): []byte("ref-" + project),
	}
	for k, v := range objs {
		if err := store.PutObject(ctx, sourceBucket, k, v); err != nil {
			t.Fatalf("deploy %s: %v", project, err)
		}
	}
}

// repoPrefix is the key fragment that marks a project's presence in the registry.
func repoPrefix(project string) string {
	return "/docker/registry/v2/repositories/" + project + "/"
}

func manifestHasProject(m *s3snap.Manifest, project string) bool {
	for _, o := range m.Objects {
		if strings.Contains(o.Key, repoPrefix(project)) {
			return true
		}
	}
	return false
}

func sourceHasProject(t *testing.T, store *memstore.Store, project string) bool {
	t.Helper()
	objs, err := store.ListObjects(context.Background(), sourceBucket, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if strings.Contains("/"+o.Key, repoPrefix(project)) {
			return true
		}
	}
	return false
}

// --- unit tests ------------------------------------------------------------

func TestNewValidation(t *testing.T) {
	store := memstore.New()
	_, err := s3snap.New(store, s3snap.Config{SnapshotBucket: "s", RegistryID: "r"})
	if !errors.Is(err, s3snap.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig, got %v", err)
	}
}

func TestSnapshotRejectsBadID(t *testing.T) {
	s, _ := newFixture(t)
	if _, err := s.Snapshot(context.Background(), "bad id!"); !errors.Is(err, s3snap.ErrInvalidSnapshotID) {
		t.Fatalf("want ErrInvalidSnapshotID, got %v", err)
	}
}

func TestSnapshotNoOverwrite(t *testing.T) {
	s, store := newFixture(t)
	deployProject(t, store, "test1")
	if _, err := s.Snapshot(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(context.Background(), "t1"); !errors.Is(err, s3snap.ErrSnapshotExists) {
		t.Fatalf("want ErrSnapshotExists, got %v", err)
	}
}

func TestSnapshotIsIncremental(t *testing.T) {
	s, store := newFixture(t)
	ctx := context.Background()

	deployProject(t, store, "test1")
	m1, err := s.Snapshot(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}

	// No source change -> second snapshot mirrors nothing new, same object set.
	m2, err := s.Snapshot(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if m1.ObjectCount != m2.ObjectCount {
		t.Fatalf("expected identical counts, got t1=%d t2=%d", m1.ObjectCount, m2.ObjectCount)
	}

	// Adding a project adds exactly its new objects.
	deployProject(t, store, "test2")
	m3, err := s.Snapshot(ctx, "t3")
	if err != nil {
		t.Fatal(err)
	}
	if m3.ObjectCount <= m2.ObjectCount {
		t.Fatalf("expected t3 to grow, got t2=%d t3=%d", m2.ObjectCount, m3.ObjectCount)
	}
}

func TestVerifyDetectsDrift(t *testing.T) {
	s, store := newFixture(t)
	ctx := context.Background()
	deployProject(t, store, "test1")
	if _, err := s.Snapshot(ctx, "t1"); err != nil {
		t.Fatal(err)
	}

	// A clean in-place verify passes.
	res, err := s.Verify(ctx, "t1", s3snap.VerifyOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("verify should pass, got %+v", res)
	}

	// Deleting a source object makes it fail with a missing object.
	_ = store.DeleteObject(ctx, sourceBucket, "docker/registry/v2/blobs/sha256/ba/base/data")
	res, err = s.Verify(ctx, "t1", s3snap.VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Missing != 1 {
		t.Fatalf("verify should report 1 missing, got %+v", res)
	}
}

// TestGCSafety shows an old snapshot stays restorable after its blobs are deleted
// from the source (the garbage-collection scenario).
func TestGCSafety(t *testing.T) {
	s, store := newFixture(t)
	ctx := context.Background()

	deployProject(t, store, "test1")
	if _, err := s.Snapshot(ctx, "t1"); err != nil {
		t.Fatal(err)
	}

	// Garbage-collect the source: wipe everything.
	for _, o := range mustList(t, store, sourceBucket) {
		_ = store.DeleteObject(ctx, sourceBucket, o.Key)
	}
	if sourceHasProject(t, store, "test1") {
		t.Fatal("source should be empty after GC")
	}

	// Restoring t1 brings it back intact.
	if _, err := s.Restore(ctx, "t1", s3snap.RestoreOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	if !sourceHasProject(t, store, "test1") {
		t.Fatal("test1 should be restored after GC")
	}
	res, _ := s.Verify(ctx, "t1", s3snap.VerifyOptions{Strict: true})
	if !res.OK {
		t.Fatalf("verify after GC restore failed: %+v", res)
	}
}

func mustList(t *testing.T, store *memstore.Store, bucket string) []s3snap.ObjectInfo {
	t.Helper()
	objs, err := store.ListObjects(context.Background(), bucket, "")
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

// --- plan 4.1–4.9 replay ---------------------------------------------------

// TestPlan reproduces, purely in-memory, the exact flow validated against the real
// cluster in informe-test-snapshots-s3-registry-hcp.md: three projects, three
// snapshots, and in-place point-in-time restores with prune.
func TestPlan(t *testing.T) {
	s, store := newFixture(t)
	ctx := context.Background()

	has := func(m *s3snap.Manifest, p string) { // manifest assertion
		t.Helper()
		if !manifestHasProject(m, p) {
			t.Fatalf("snapshot %s should contain %s", m.SnapshotID, p)
		}
	}
	lacks := func(m *s3snap.Manifest, p string) {
		t.Helper()
		if manifestHasProject(m, p) {
			t.Fatalf("snapshot %s should not contain %s", m.SnapshotID, p)
		}
	}
	srcHas := func(p string) {
		t.Helper()
		if !sourceHasProject(t, store, p) {
			t.Fatalf("source should contain %s", p)
		}
	}
	srcLacks := func(p string) {
		t.Helper()
		if sourceHasProject(t, store, p) {
			t.Fatalf("source should not contain %s", p)
		}
	}
	verifyStrict := func(id string) {
		t.Helper()
		res, err := s.Verify(ctx, id, s3snap.VerifyOptions{Strict: true})
		if err != nil {
			t.Fatal(err)
		}
		if !res.OK {
			t.Fatalf("verify --strict: source != %s: %+v", id, res)
		}
	}

	// 4.1–4.2
	deployProject(t, store, "test1")
	t1, err := s.Snapshot(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	has(t1, "test1")
	lacks(t1, "test2")
	lacks(t1, "test3")

	// 4.3–4.4
	deployProject(t, store, "test2")
	t2, err := s.Snapshot(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	has(t2, "test1")
	has(t2, "test2")
	lacks(t2, "test3")

	// 4.5–4.6
	deployProject(t, store, "test3")
	t3, err := s.Snapshot(ctx, "t3")
	if err != nil {
		t.Fatal(err)
	}
	has(t3, "test1")
	has(t3, "test2")
	has(t3, "test3")

	// ListSnapshots sees all three, oldest first.
	all, err := s.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].SnapshotID != "t1" || all[2].SnapshotID != "t3" {
		t.Fatalf("ListSnapshots = %v", snapshotIDs(all))
	}

	// 4.7 restore to t2 (in place, prune)
	if _, err := s.Restore(ctx, "t2", s3snap.RestoreOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	srcHas("test1")
	srcHas("test2")
	srcLacks("test3")
	verifyStrict("t2")

	// 4.8 restore to t1
	if _, err := s.Restore(ctx, "t1", s3snap.RestoreOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	srcHas("test1")
	srcLacks("test2")
	srcLacks("test3")
	verifyStrict("t1")

	// 4.9 restore to t3
	if _, err := s.Restore(ctx, "t3", s3snap.RestoreOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	srcHas("test1")
	srcHas("test2")
	srcHas("test3")
	verifyStrict("t3")
}

func snapshotIDs(ms []s3snap.Manifest) []string {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.SnapshotID
	}
	return ids
}

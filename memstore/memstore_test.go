package memstore_test

import (
	"context"
	"testing"

	"github.com/lucasponce/s3snap"
	"github.com/lucasponce/s3snap/memstore"
)

// These tests document the versioning semantics the rest of s3snap relies on, so
// the fake can be trusted as a stand-in for S3.

func TestPutHeadGet(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	if err := s.PutObject(ctx, "b", "k", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	info, ok, err := s.HeadObject(ctx, "b", "k")
	if err != nil || !ok {
		t.Fatalf("head: ok=%v err=%v", ok, err)
	}
	if info.Size != 5 {
		t.Errorf("size = %d, want 5", info.Size)
	}

	data, err := s.GetObject(ctx, s3snap.ObjectRef{Bucket: "b", Key: "k"})
	if err != nil || string(data) != "hello" {
		t.Fatalf("get = %q err=%v", data, err)
	}
}

func TestListExcludesDeleted(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	_ = s.PutObject(ctx, "b", "keep", []byte("a"))
	_ = s.PutObject(ctx, "b", "gone", []byte("b"))
	_ = s.DeleteObject(ctx, "b", "gone")

	objs, err := s.ListObjects(ctx, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Key != "keep" {
		t.Fatalf("list = %+v, want only 'keep'", objs)
	}

	if _, ok, _ := s.HeadObject(ctx, "b", "gone"); ok {
		t.Error("deleted object should not be found by head")
	}
}

// TestOldVersionSurvivesDelete is the garbage-collection-safety property: a
// specific past version stays readable by id even after the key is deleted.
func TestOldVersionSurvivesDelete(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	_ = s.PutObject(ctx, "b", "k", []byte("v1"))
	v1, _, _ := s.HeadObject(ctx, "b", "k")

	_ = s.DeleteObject(ctx, "b", "k")

	// Current version is gone...
	if _, ok, _ := s.HeadObject(ctx, "b", "k"); ok {
		t.Fatal("key should read as deleted")
	}
	// ...but the pinned version is still retrievable.
	data, err := s.GetObject(ctx, s3snap.ObjectRef{Bucket: "b", Key: "k", VersionID: v1.VersionID})
	if err != nil || string(data) != "v1" {
		t.Fatalf("get pinned version = %q err=%v", data, err)
	}
}

func TestListByPrefix(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	_ = s.PutObject(ctx, "b", "a/1", []byte("x"))
	_ = s.PutObject(ctx, "b", "a/2", []byte("x"))
	_ = s.PutObject(ctx, "b", "c/3", []byte("x"))

	objs, _ := s.ListObjects(ctx, "b", "a/")
	if len(objs) != 2 {
		t.Fatalf("got %d objects under a/, want 2", len(objs))
	}
}

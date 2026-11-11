// Package memstore provides an in-memory, versioned implementation of
// s3snap.ObjectStore.
//
// It models just enough of S3's versioning semantics to exercise s3snap's logic
// without touching AWS: every write creates a new version, deletes add a delete
// marker, and a specific past version stays readable by its id even after the key
// is overwritten or deleted. That last property is what lets the tests show the
// scheme is garbage-collection safe.
//
// It is intended for tests and examples, and is safe for concurrent use.
package memstore

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"
	"sync"

	"github.com/lucasponce/s3snap"
)

// version is one stored version of a key.
type version struct {
	id      string
	data    []byte
	etag    string
	deleted bool // true for a delete marker
}

// Store is an in-memory object store with versioning. The zero value is not
// usable; call New.
type Store struct {
	mu      sync.Mutex
	buckets map[string]map[string][]version // bucket -> key -> versions (oldest first)
	seq     int
}

// New returns an empty Store.
func New() *Store {
	return &Store{buckets: make(map[string]map[string][]version)}
}

var _ s3snap.ObjectStore = (*Store)(nil)

func (s *Store) nextVersionID() string {
	s.seq++
	return fmt.Sprintf("v%d", s.seq)
}

// bucketMap returns the key->versions map for a bucket, creating it on demand.
func (s *Store) bucketMap(bucket string) map[string][]version {
	m, ok := s.buckets[bucket]
	if !ok {
		m = make(map[string][]version)
		s.buckets[bucket] = m
	}
	return m
}

// appendVersion stores a new version for bucket/key and returns its id.
func (s *Store) appendVersion(bucket, key string, v version) {
	m := s.bucketMap(bucket)
	m[key] = append(m[key], v)
}

// latest returns the most recent version of bucket/key, or ok=false if the key
// has no versions.
func latest(versions []version) (version, bool) {
	if len(versions) == 0 {
		return version{}, false
	}
	return versions[len(versions)-1], true
}

func etagOf(data []byte) string {
	return fmt.Sprintf("%q", fmt.Sprintf("%x", md5.Sum(data)))
}

// ListObjects returns the current, non-deleted version of every key under prefix.
func (s *Store) ListObjects(_ context.Context, bucket, prefix string) ([]s3snap.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []s3snap.ObjectInfo
	for key, versions := range s.buckets[bucket] {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		v, ok := latest(versions)
		if !ok || v.deleted {
			continue
		}
		out = append(out, s3snap.ObjectInfo{
			Key:       key,
			VersionID: v.id,
			Size:      int64(len(v.data)),
			ETag:      v.etag,
		})
	}
	return out, nil
}

// CopyObject copies src (a specific version when set, else the current one) to
// dstBucket/dstKey as a new version.
func (s *Store) CopyObject(_ context.Context, src s3snap.ObjectRef, dstBucket, dstKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, err := s.findLocked(src)
	if err != nil {
		return err
	}
	data := append([]byte(nil), v.data...)
	s.appendVersion(dstBucket, dstKey, version{id: s.nextVersionID(), data: data, etag: etagOf(data)})
	return nil
}

// GetObject returns the bytes of the referenced object (a specific version when
// set, else the current one).
func (s *Store) GetObject(_ context.Context, ref s3snap.ObjectRef) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, err := s.findLocked(ref)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), v.data...), nil
}

// findLocked resolves ref to a concrete, non-deleted version. The caller must
// hold s.mu.
func (s *Store) findLocked(ref s3snap.ObjectRef) (version, error) {
	versions := s.buckets[ref.Bucket][ref.Key]
	if ref.VersionID != "" {
		for _, v := range versions {
			if v.id == ref.VersionID {
				if v.deleted {
					return version{}, fmt.Errorf("memstore: %s/%s version %s is a delete marker", ref.Bucket, ref.Key, ref.VersionID)
				}
				return v, nil
			}
		}
		return version{}, fmt.Errorf("memstore: %s/%s version %s not found", ref.Bucket, ref.Key, ref.VersionID)
	}
	v, ok := latest(versions)
	if !ok || v.deleted {
		return version{}, fmt.Errorf("memstore: %s/%s not found", ref.Bucket, ref.Key)
	}
	return v, nil
}

// PutObject writes data as a new version of bucket/key.
func (s *Store) PutObject(_ context.Context, bucket, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := append([]byte(nil), data...)
	s.appendVersion(bucket, key, version{id: s.nextVersionID(), data: stored, etag: etagOf(stored)})
	return nil
}

// DeleteObject adds a delete marker for the current version of bucket/key.
// Previous versions remain readable by version id.
func (s *Store) DeleteObject(_ context.Context, bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if v, ok := latest(s.buckets[bucket][key]); !ok || v.deleted {
		return nil // nothing to delete; idempotent
	}
	s.appendVersion(bucket, key, version{id: s.nextVersionID(), deleted: true})
	return nil
}

// HeadObject returns metadata for the current version of bucket/key.
func (s *Store) HeadObject(_ context.Context, bucket, key string) (s3snap.ObjectInfo, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := latest(s.buckets[bucket][key])
	if !ok || v.deleted {
		return s3snap.ObjectInfo{}, false, nil
	}
	return s3snap.ObjectInfo{
		Key:       key,
		VersionID: v.id,
		Size:      int64(len(v.data)),
		ETag:      v.etag,
	}, true, nil
}

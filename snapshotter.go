package s3snap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Snapshotter takes and restores point-in-time snapshots of Config.SourceBucket
// using Config.SnapshotBucket as durable storage. It is the main entry point of
// the package.
//
// A Snapshotter is stateless beyond its config and store; it is safe to reuse for
// many operations.
type Snapshotter struct {
	store ObjectStore
	cfg   Config
	// now is overridable in tests; defaults to time.Now.
	now func() time.Time
}

// New returns a Snapshotter backed by store. It validates cfg and returns
// ErrInvalidConfig if any required field is missing.
func New(store ObjectStore, cfg Config) (*Snapshotter, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is required", ErrInvalidConfig)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Snapshotter{store: store, cfg: cfg, now: time.Now}, nil
}

// Snapshot takes a point-in-time snapshot under the given id. It first mirrors any
// new source objects into the snapshot bucket, then writes a manifest recording
// the current version of every mirrored object.
//
// It returns ErrSnapshotExists if a snapshot with this id already exists.
func (s *Snapshotter) Snapshot(ctx context.Context, id string) (*Manifest, error) {
	if err := validateSnapshotID(id); err != nil {
		return nil, err
	}

	// Never overwrite an existing snapshot.
	if _, ok, err := s.store.HeadObject(ctx, s.cfg.SnapshotBucket, s.cfg.manifestKey(id)); err != nil {
		return nil, fmt.Errorf("checking for existing snapshot %q: %w", id, err)
	} else if ok {
		return nil, fmt.Errorf("%w: %q", ErrSnapshotExists, id)
	}

	// Step 1: incremental content sync (source -> snapshotBucket/<registryID>/).
	if err := s.sync(ctx); err != nil {
		return nil, err
	}

	// Step 2: build the manifest from the current versions now in the snapshot
	// bucket. These version ids are exactly what Restore will copy back.
	mirrored, err := s.store.ListObjects(ctx, s.cfg.SnapshotBucket, s.cfg.dataPrefix())
	if err != nil {
		return nil, fmt.Errorf("listing snapshot content: %w", err)
	}
	sort.Slice(mirrored, func(i, j int) bool { return mirrored[i].Key < mirrored[j].Key })

	m := &Manifest{
		SnapshotID:     id,
		TakenAt:        s.now().UTC(),
		RegistryID:     s.cfg.RegistryID,
		SourceBucket:   s.cfg.SourceBucket,
		SnapshotBucket: s.cfg.SnapshotBucket,
		ObjectCount:    len(mirrored),
		Objects:        make([]ObjectMeta, 0, len(mirrored)),
	}
	for _, o := range mirrored {
		m.Objects = append(m.Objects, ObjectMeta{
			Key:       o.Key,
			VersionID: o.VersionID,
			Size:      o.Size,
			ETag:      o.ETag,
		})
	}

	data, err := encodeManifest(m)
	if err != nil {
		return nil, err
	}
	if err := s.store.PutObject(ctx, s.cfg.SnapshotBucket, s.cfg.manifestKey(id), data); err != nil {
		return nil, fmt.Errorf("writing manifest for %q: %w", id, err)
	}
	return m, nil
}

// sync copies into the snapshot bucket every source object that is not already
// there with the same size. It never deletes: because registry blobs are
// immutable, objects only accumulate, which keeps previously taken snapshots
// valid even after the source is garbage-collected.
func (s *Snapshotter) sync(ctx context.Context) error {
	srcObjs, err := s.store.ListObjects(ctx, s.cfg.SourceBucket, "")
	if err != nil {
		return fmt.Errorf("listing source bucket: %w", err)
	}
	snapObjs, err := s.store.ListObjects(ctx, s.cfg.SnapshotBucket, s.cfg.dataPrefix())
	if err != nil {
		return fmt.Errorf("listing snapshot bucket: %w", err)
	}

	// Index what is already mirrored, by the source-relative key.
	have := make(map[string]int64, len(snapObjs))
	for _, o := range snapObjs {
		have[strings.TrimPrefix(o.Key, s.cfg.dataPrefix())] = o.Size
	}

	for _, o := range srcObjs {
		if size, ok := have[o.Key]; ok && size == o.Size {
			continue // already mirrored, unchanged
		}
		dstKey := s.cfg.dataPrefix() + o.Key
		src := ObjectRef{Bucket: s.cfg.SourceBucket, Key: o.Key, VersionID: o.VersionID}
		if err := s.store.CopyObject(ctx, src, s.cfg.SnapshotBucket, dstKey); err != nil {
			return fmt.Errorf("mirroring %q: %w", o.Key, err)
		}
	}
	return nil
}

// GetManifest reads and parses the manifest for a snapshot id. It returns
// ErrSnapshotNotFound if no such snapshot exists.
func (s *Snapshotter) GetManifest(ctx context.Context, id string) (*Manifest, error) {
	if _, ok, err := s.store.HeadObject(ctx, s.cfg.SnapshotBucket, s.cfg.manifestKey(id)); err != nil {
		return nil, fmt.Errorf("checking snapshot %q: %w", id, err)
	} else if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSnapshotNotFound, id)
	}
	data, err := s.store.GetObject(ctx, ObjectRef{Bucket: s.cfg.SnapshotBucket, Key: s.cfg.manifestKey(id)})
	if err != nil {
		return nil, fmt.Errorf("reading manifest for %q: %w", id, err)
	}
	return decodeManifest(data)
}

// ListSnapshots returns the manifests of all snapshots taken for this source,
// sorted by time taken (oldest first).
func (s *Snapshotter) ListSnapshots(ctx context.Context) ([]Manifest, error) {
	objs, err := s.store.ListObjects(ctx, s.cfg.SnapshotBucket, s.cfg.manifestPrefix())
	if err != nil {
		return nil, fmt.Errorf("listing manifests: %w", err)
	}
	out := make([]Manifest, 0, len(objs))
	for _, o := range objs {
		id := s.cfg.snapshotIDFromManifestKey(o.Key)
		if id == "" {
			continue
		}
		m, err := s.GetManifest(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TakenAt.Before(out[j].TakenAt) })
	return out, nil
}

// RestoreOptions configures a Restore.
type RestoreOptions struct {
	// TargetBucket is where the snapshot is restored. When empty, it defaults to
	// Config.SourceBucket (restore in place).
	TargetBucket string
	// Prune, when true, deletes from the target any object that is not part of the
	// snapshot, leaving the target exactly as it was at snapshot time.
	Prune bool
}

// RestoreResult reports what a Restore did.
type RestoreResult struct {
	Copied int
	Pruned int
}

// Restore copies every object recorded in the snapshot's manifest into the target
// bucket, stripping the <registryID>/ prefix so the target gets the original key
// layout. With Prune enabled it also removes objects not in the snapshot.
func (s *Snapshotter) Restore(ctx context.Context, id string, opts RestoreOptions) (*RestoreResult, error) {
	m, err := s.GetManifest(ctx, id)
	if err != nil {
		return nil, err
	}
	target := opts.TargetBucket
	if target == "" {
		target = s.cfg.SourceBucket
	}

	res := &RestoreResult{}
	expected := make(map[string]struct{}, len(m.Objects))
	for _, o := range m.Objects {
		rel := strings.TrimPrefix(o.Key, s.cfg.dataPrefix())
		expected[rel] = struct{}{}
		src := ObjectRef{Bucket: s.cfg.SnapshotBucket, Key: o.Key, VersionID: o.VersionID}
		if err := s.store.CopyObject(ctx, src, target, rel); err != nil {
			return nil, fmt.Errorf("restoring %q: %w", rel, err)
		}
		res.Copied++
	}

	if opts.Prune {
		current, err := s.store.ListObjects(ctx, target, "")
		if err != nil {
			return nil, fmt.Errorf("listing target for prune: %w", err)
		}
		for _, o := range current {
			if _, keep := expected[o.Key]; keep {
				continue
			}
			if err := s.store.DeleteObject(ctx, target, o.Key); err != nil {
				return nil, fmt.Errorf("pruning %q: %w", o.Key, err)
			}
			res.Pruned++
		}
	}
	return res, nil
}

// VerifyOptions configures a Verify.
type VerifyOptions struct {
	// TargetBucket is the bucket to check. When empty, it defaults to
	// Config.SourceBucket.
	TargetBucket string
	// Strict, when true, also flags objects present in the target but absent from
	// the snapshot (extras).
	Strict bool
}

// VerifyResult reports how a target bucket compares to a snapshot.
type VerifyResult struct {
	Matched  int
	Missing  int
	Mismatch int
	Extra    int
	// OK is true when the target matches the snapshot (no missing or mismatched
	// objects, and no extras when Strict).
	OK bool
}

// Verify checks that the target bucket matches a snapshot. Sizes must always
// match; ETags are compared only when both are single-part (an MD5), because a
// server-side copy does not preserve a multipart ETag even when the bytes are
// identical.
func (s *Snapshotter) Verify(ctx context.Context, id string, opts VerifyOptions) (*VerifyResult, error) {
	m, err := s.GetManifest(ctx, id)
	if err != nil {
		return nil, err
	}
	target := opts.TargetBucket
	if target == "" {
		target = s.cfg.SourceBucket
	}

	res := &VerifyResult{}
	expected := make(map[string]struct{}, len(m.Objects))
	for _, o := range m.Objects {
		rel := strings.TrimPrefix(o.Key, s.cfg.dataPrefix())
		expected[rel] = struct{}{}
		info, ok, err := s.store.HeadObject(ctx, target, rel)
		if err != nil {
			return nil, fmt.Errorf("heading %q: %w", rel, err)
		}
		if !ok {
			res.Missing++
			continue
		}
		if info.Size != o.Size {
			res.Mismatch++
			continue
		}
		if isSinglePartETag(info.ETag) && isSinglePartETag(o.ETag) && info.ETag != o.ETag {
			res.Mismatch++
			continue
		}
		res.Matched++
	}

	if opts.Strict {
		current, err := s.store.ListObjects(ctx, target, "")
		if err != nil {
			return nil, fmt.Errorf("listing target: %w", err)
		}
		for _, o := range current {
			if _, ok := expected[o.Key]; !ok {
				res.Extra++
			}
		}
	}

	res.OK = res.Missing == 0 && res.Mismatch == 0 && (!opts.Strict || res.Extra == 0)
	return res, nil
}

// isSinglePartETag reports whether etag is a plain MD5 (single-part upload) rather
// than a multipart ETag, which carries a "-<partcount>" suffix.
func isSinglePartETag(etag string) bool {
	return etag != "" && !strings.Contains(etag, "-")
}

package s3snap

import "context"

// ObjectInfo describes a single object (the current version of a key) as seen in
// a bucket listing or a HEAD request.
type ObjectInfo struct {
	Key string
	// VersionID is the S3 version id of this object. It is empty for buckets
	// without versioning enabled.
	VersionID string
	Size      int64
	// ETag is the object's entity tag, as returned by S3 (quoted). For objects
	// uploaded in a single part it is the MD5 of the content; for multipart
	// uploads it has a "-<partcount>" suffix and is not an MD5.
	ETag string
}

// ObjectRef points at an object to read or copy. VersionID is optional: when
// empty, the current version is used.
type ObjectRef struct {
	Bucket    string
	Key       string
	VersionID string
}

// ObjectStore is the minimal object-storage surface s3snap needs. It is small on
// purpose: it is the only thing the core logic depends on, so it is easy to
// implement against the AWS SDK (see package awsstore) and easy to fake for tests
// (see package memstore).
//
// Implementations must be safe for the single-goroutine use the Snapshotter makes
// of them; they need not be concurrency-safe.
type ObjectStore interface {
	// ListObjects returns the current version of every object whose key starts
	// with prefix (use "" for the whole bucket). Deleted objects are not returned.
	ListObjects(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error)

	// CopyObject server-side-copies src (optionally a specific version) to
	// dstBucket/dstKey.
	CopyObject(ctx context.Context, src ObjectRef, dstBucket, dstKey string) error

	// GetObject returns the bytes of the referenced object. If ref.VersionID is
	// set, that specific version is read even if the current version differs or
	// the key has since been deleted.
	GetObject(ctx context.Context, ref ObjectRef) ([]byte, error)

	// PutObject writes data to bucket/key, creating a new version if the bucket is
	// versioned.
	PutObject(ctx context.Context, bucket, key string, data []byte) error

	// DeleteObject deletes the current version of bucket/key. On a versioned
	// bucket this adds a delete marker and previous versions remain retrievable by
	// version id.
	DeleteObject(ctx context.Context, bucket, key string) error

	// HeadObject returns metadata for the current version of bucket/key. ok is
	// false (with a nil error) when the key does not exist or is deleted.
	HeadObject(ctx context.Context, bucket, key string) (info ObjectInfo, ok bool, err error)
}

package s3snap

import "errors"

// Sentinel errors returned by the package. Callers can test for them with
// errors.Is.
var (
	// ErrInvalidConfig is returned by New when the Config is incomplete.
	ErrInvalidConfig = errors.New("s3snap: invalid config")
	// ErrInvalidSnapshotID is returned when a snapshot id contains characters that
	// are not safe to embed in an object key.
	ErrInvalidSnapshotID = errors.New("s3snap: invalid snapshot id")
	// ErrSnapshotExists is returned by Snapshot when a snapshot with the same id
	// already exists; snapshots are never silently overwritten.
	ErrSnapshotExists = errors.New("s3snap: snapshot already exists")
	// ErrSnapshotNotFound is returned when a referenced snapshot does not exist.
	ErrSnapshotNotFound = errors.New("s3snap: snapshot not found")
)

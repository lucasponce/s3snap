package s3snap

import (
	"fmt"
	"regexp"
	"strings"
)

// Config holds the buckets and identity s3snap operates on. All fields are
// required.
type Config struct {
	// SourceBucket is the bucket being snapshotted (e.g. the registry bucket).
	SourceBucket string
	// SnapshotBucket is the durable, versioning-enabled bucket where snapshots and
	// their manifests are stored.
	SnapshotBucket string
	// RegistryID is the logical identifier used as the key prefix under which this
	// source's data and manifests live inside the snapshot bucket. It lets one
	// snapshot bucket hold snapshots for several sources without collisions.
	RegistryID string
}

func (c Config) validate() error {
	switch {
	case c.SourceBucket == "":
		return fmt.Errorf("%w: SourceBucket is required", ErrInvalidConfig)
	case c.SnapshotBucket == "":
		return fmt.Errorf("%w: SnapshotBucket is required", ErrInvalidConfig)
	case c.RegistryID == "":
		return fmt.Errorf("%w: RegistryID is required", ErrInvalidConfig)
	}
	return nil
}

// dataPrefix is the key prefix under which the source's objects are mirrored in
// the snapshot bucket.
func (c Config) dataPrefix() string {
	return c.RegistryID + "/"
}

// manifestKey is the key of the manifest for a given snapshot id.
func (c Config) manifestKey(id string) string {
	return c.manifestPrefix() + id + ".json"
}

// manifestPrefix is the key prefix under which this source's manifests live.
func (c Config) manifestPrefix() string {
	return "manifests/" + c.RegistryID + "/"
}

// snapshotIDFromManifestKey extracts the snapshot id from a manifest key, or ""
// if the key is not a manifest of this source.
func (c Config) snapshotIDFromManifestKey(key string) string {
	if !strings.HasPrefix(key, c.manifestPrefix()) || !strings.HasSuffix(key, ".json") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(key, c.manifestPrefix()), ".json")
}

// validSnapshotID matches ids that are safe to embed in an object key.
var validSnapshotID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateSnapshotID(id string) error {
	if !validSnapshotID.MatchString(id) {
		return fmt.Errorf("%w: %q (allowed: letters, digits, '.', '_', '-')", ErrInvalidSnapshotID, id)
	}
	return nil
}

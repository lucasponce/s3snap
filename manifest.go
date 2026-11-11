package s3snap

import (
	"encoding/json"
	"fmt"
	"time"
)

// Manifest is the on-disk representation of a snapshot: the set of version
// pointers that, together, define the state of the source bucket at TakenAt.
//
// Its JSON layout is intentionally identical to the one produced by the shell
// prototype, so manifests are interchangeable between the two.
type Manifest struct {
	SnapshotID     string       `json:"snapshot_id"`
	TakenAt        time.Time    `json:"taken_at"`
	RegistryID     string       `json:"registry_id"`
	SourceBucket   string       `json:"source_bucket"`
	SnapshotBucket string       `json:"snapshot_bucket"`
	ObjectCount    int          `json:"object_count"`
	Objects        []ObjectMeta `json:"objects"`
}

// ObjectMeta is one entry in a Manifest: a pointer to a specific version of one
// object in the snapshot bucket. Key includes the <registryID>/ prefix.
type ObjectMeta struct {
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	Size      int64  `json:"size"`
	ETag      string `json:"etag"`
}

// encodeManifest serializes m as indented JSON.
func encodeManifest(m *Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	return data, nil
}

// decodeManifest parses a manifest from JSON.
func decodeManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	return &m, nil
}

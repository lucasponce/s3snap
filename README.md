# s3snap

Go library to take and restore **point-in-time snapshots of an S3 bucket at the bucket
level**, independently of the source bucket's lifetime.

It was built to back up the internal image registry of a ROSA HCP cluster (whose blobs
live in an S3 bucket) across a `HostedCluster` destroy/restore, but it is agnostic to the
bucket contents and is meant to be **imported by other projects** — for example a Velero
plugin that coordinates these snapshots with its backups and restores.

This is the Go port of a shell prototype that was validated end-to-end against a real
cluster; the same flow is reproduced here by `TestPlan`.

## Design in one paragraph

There is no native S3 call that snapshots a bucket. s3snap builds one on top of three
primitives: a **durable, versioning-enabled snapshot bucket** separate from the source; an
incremental **content sync** (`source → snapshotBucket/<registryID>/`, add-only, cheap
because registry blobs are immutable); and a **manifest per point in time**
(`manifests/<registryID>/<id>.json`) that records the current `VersionId` of every object.
The manifest *is* the snapshot — a set of pointers, not a copy. Restoring copies each
`(key, versionId)` into a target bucket (any name), stripping the `<registryID>/` prefix.
Versioning on the snapshot bucket makes it garbage-collection safe: a blob pruned from the
source after a snapshot stays retained as a non-current version, still referenced by the
older manifest.

## Layout

| Package | Purpose |
|---|---|
| `s3snap` | Core logic (`Snapshotter`: `Snapshot`, `ListSnapshots`, `Restore`, `Verify`). Depends only on the `ObjectStore` interface — **no AWS dependency**. |
| `s3snap/awsstore` | `ObjectStore` backed by AWS SDK for Go v2, plus `EnsureVersionedBucket`. |
| `s3snap/memstore` | In-memory, versioned `ObjectStore` for tests and examples. |

The core depends on a small interface:

```go
type ObjectStore interface {
    ListObjects(ctx, bucket, prefix string) ([]ObjectInfo, error)
    CopyObject(ctx, src ObjectRef, dstBucket, dstKey string) error
    GetObject(ctx, ref ObjectRef) ([]byte, error)
    PutObject(ctx, bucket, key string, data []byte) error
    DeleteObject(ctx, bucket, key string) error
    HeadObject(ctx, bucket, key string) (ObjectInfo, bool, error)
}
```

## Usage

```go
import (
    awsconfig "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/s3"

    "github.com/lucasponce/s3snap"
    "github.com/lucasponce/s3snap/awsstore"
)

cfg, _ := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-west-2"))
client := s3.NewFromConfig(cfg)

// Once: provision the durable snapshot bucket.
_ = awsstore.EnsureVersionedBucket(ctx, client, "my-snapshots", awsstore.BucketOptions{
    Region: "us-west-2", ExpireNoncurrentDays: 30,
})

snap, _ := s3snap.New(awsstore.New(client), s3snap.Config{
    SourceBucket:   "<infra-id>-image-registry-us-west-2",
    SnapshotBucket: "my-snapshots",
    RegistryID:     "<infra-id>",
})

snap.Snapshot(ctx, "t1")                                              // take a snapshot
snap.Restore(ctx, "t1", s3snap.RestoreOptions{Prune: true})          // restore in place
snap.Restore(ctx, "t1", s3snap.RestoreOptions{TargetBucket: "new"})  // restore to another bucket
snap.Verify(ctx, "t1", s3snap.VerifyOptions{Strict: true})           // check a bucket matches
```

A Velero plugin that already has a configured `*s3.Client` just wraps it with
`awsstore.New(client)` and passes it to `s3snap.New`.

## Tests

The tests are written to be read: `memstore` models S3 versioning, and the `s3snap` tests
exercise the business logic against it — no AWS required.

```bash
go test ./...                       # unit tests + the plan 4.1–4.9 replay (in-memory)
```

- `TestPlan` reproduces, in-memory, the exact flow validated against the real cluster in
  `informe-test-snapshots-s3-registry-hcp.md`: three projects, three snapshots, and
  in-place point-in-time restores with prune.
- `TestGCSafety` shows an old snapshot stays restorable after its source blobs are deleted.
- `memstore` has its own tests so the fake can be trusted.

Optional real-S3 integration test (creates and deletes its own short-lived buckets, so it
never touches a real registry):

```bash
S3SNAP_IT=1 AWS_REGION=us-west-2 go test -tags integration -run TestIntegration -v
```

## Limitations / next iterations

- `CopyObject` uses a single server-side copy (limit 5 GiB). Registry blobs are far smaller;
  multipart copy for larger objects is left for later.
- `SourceBucket`, `SnapshotBucket` and a restore target are assumed reachable by one S3
  client (same region in practice). Cross-region is the caller's concern for now.
- Snapshot consistency assumes no *critical* concurrent writes during the sync; for an
  immutable content-addressed registry a concurrent write only adds new blobs.

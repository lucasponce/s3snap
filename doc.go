// Package s3snap takes and restores point-in-time snapshots of an S3 bucket at
// the bucket level, independently of the source bucket's lifetime.
//
// It was built to back up the internal image registry of a ROSA HCP cluster
// (whose blobs live in an S3 bucket) across a HostedCluster destroy/restore, but
// it is agnostic to the bucket contents and is meant to be imported by other
// projects, such as a Velero plugin that coordinates these snapshots with its
// backups and restores.
//
// # Design
//
// There is no native S3 call that "snapshots a bucket". s3snap builds one on top
// of three primitives:
//
//  1. A durable, long-lived snapshot bucket with versioning enabled, separate
//     from the source bucket so it survives the source being deleted.
//  2. A content sync: each snapshot first copies new objects from the source
//     bucket into snapshotBucket/<registryID>/ using the same key layout. Because
//     registry blobs are content-addressed and immutable, this only ever adds
//     objects, so the copy is incremental and cheap.
//  3. A manifest per point in time, stored at
//     manifests/<registryID>/<snapshotID>.json, recording the current VersionId
//     of every object at that moment. The manifest *is* the snapshot: a set of
//     pointers, not a copy of the data.
//
// Restoring a snapshot copies each (key, versionId) listed in its manifest into a
// target bucket, stripping the <registryID>/ prefix. Because the restore starts
// from explicit version pointers, it works regardless of the target bucket name,
// which cleanly handles "the cluster was recreated, so its registry bucket has a
// different name".
//
// Versioning on the snapshot bucket is what makes the scheme garbage-collection
// safe: if a blob is pruned from the source after a snapshot was taken, the blob
// stays retained in the snapshot bucket as a non-current version, still referenced
// by the older manifest.
//
// # Layering
//
// The core logic in this package depends only on the ObjectStore interface, so it
// has no AWS dependency and is trivial to test. Package awsstore provides an
// ObjectStore backed by the AWS SDK for Go v2; package memstore provides an
// in-memory, versioned ObjectStore used by the tests (and usable by consumers to
// test their own integration).
package s3snap

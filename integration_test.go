//go:build integration

// Integration test that runs the snapshot/restore/verify cycle against real S3.
//
// It is opt-in and excluded from normal `go test` by the build tag. Run it with:
//
//	S3SNAP_IT=1 AWS_REGION=us-west-2 go test -tags integration -run TestIntegration -v
//
// It needs AWS credentials (standard SDK chain) allowed to create and delete
// buckets. It creates two short-lived buckets of its own (a source and a durable
// snapshot bucket), exercises the full cycle on them, and deletes them at the end,
// so it never touches a real registry bucket.
package s3snap_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/lucasponce/s3snap"
	"github.com/lucasponce/s3snap/awsstore"
)

func TestIntegration(t *testing.T) {
	if os.Getenv("S3SNAP_IT") == "" {
		t.Skip("set S3SNAP_IT=1 to run the real-S3 integration test")
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-west-2"
	}
	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	client := s3.NewFromConfig(cfg)
	store := awsstore.New(client)

	run := time.Now().UTC().Format("20060102-150405")
	src := fmt.Sprintf("s3snap-it-src-%s", run)
	snap := fmt.Sprintf("s3snap-it-snap-%s", run)

	// Provision both buckets; register cleanup immediately so a mid-test failure
	// still tears them down.
	if err := awsstore.EnsureVersionedBucket(ctx, client, src, awsstore.BucketOptions{Region: region}); err != nil {
		t.Fatalf("create source bucket: %v", err)
	}
	t.Cleanup(func() { emptyAndDeleteBucket(t, client, src) })
	if err := awsstore.EnsureVersionedBucket(ctx, client, snap, awsstore.BucketOptions{Region: region}); err != nil {
		t.Fatalf("create snapshot bucket: %v", err)
	}
	t.Cleanup(func() { emptyAndDeleteBucket(t, client, snap) })

	s, err := s3snap.New(store, s3snap.Config{SourceBucket: src, SnapshotBucket: snap, RegistryID: "it"})
	if err != nil {
		t.Fatal(err)
	}

	put := func(key, body string) {
		if err := store.PutObject(ctx, src, key, []byte(body)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}

	put("docker/registry/v2/blobs/a/data", "object-a")
	if _, err := s.Snapshot(ctx, "t1"); err != nil {
		t.Fatalf("snapshot t1: %v", err)
	}

	put("docker/registry/v2/blobs/b/data", "object-b")
	if _, err := s.Snapshot(ctx, "t2"); err != nil {
		t.Fatalf("snapshot t2: %v", err)
	}

	// Restore t1 in place with prune: object-b must be gone, and verify passes.
	if _, err := s.Restore(ctx, "t1", s3snap.RestoreOptions{Prune: true}); err != nil {
		t.Fatalf("restore t1: %v", err)
	}
	if res, err := s.Verify(ctx, "t1", s3snap.VerifyOptions{Strict: true}); err != nil || !res.OK {
		t.Fatalf("verify t1: ok=%v res=%+v err=%v", res != nil && res.OK, res, err)
	}

	// Restore t2: object-b is back.
	if _, err := s.Restore(ctx, "t2", s3snap.RestoreOptions{Prune: true}); err != nil {
		t.Fatalf("restore t2: %v", err)
	}
	if res, err := s.Verify(ctx, "t2", s3snap.VerifyOptions{Strict: true}); err != nil || !res.OK {
		t.Fatalf("verify t2: ok=%v res=%+v err=%v", res != nil && res.OK, res, err)
	}
}

// emptyAndDeleteBucket removes every version in a bucket and then deletes it.
func emptyAndDeleteBucket(t *testing.T, client *s3.Client, bucket string) {
	t.Helper()
	ctx := context.Background()
	p := s3.NewListObjectVersionsPaginator(client, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			t.Logf("cleanup list %s: %v", bucket, err)
			return
		}
		del := func(key, versionID string) {
			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(versionID),
			}); err != nil {
				t.Logf("cleanup delete %s/%s: %v", bucket, key, err)
			}
		}
		for _, v := range page.Versions {
			del(aws.ToString(v.Key), aws.ToString(v.VersionId))
		}
		for _, d := range page.DeleteMarkers {
			del(aws.ToString(d.Key), aws.ToString(d.VersionId))
		}
	}
	if _, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Logf("cleanup delete bucket %s: %v", bucket, err)
	}
}

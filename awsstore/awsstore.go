// Package awsstore provides an s3snap.ObjectStore backed by the AWS SDK for Go v2,
// plus a helper to provision the durable snapshot bucket.
//
// A Velero plugin that already builds an *s3.Client from its own credentials can
// wrap it with New and hand the result to s3snap.New.
package awsstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"github.com/lucasponce/s3snap"
)

// Store implements s3snap.ObjectStore against S3.
type Store struct {
	client *s3.Client
}

var _ s3snap.ObjectStore = (*Store)(nil)

// New wraps an S3 client as an s3snap.ObjectStore.
func New(client *s3.Client) *Store {
	return &Store{client: client}
}

// ListObjects returns the current version of every object under prefix. It uses
// ListObjectVersions (filtered to the latest version per key) so it works on both
// versioned and unversioned buckets.
func (s *Store) ListObjects(ctx context.Context, bucket, prefix string) ([]s3snap.ObjectInfo, error) {
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)}
	if prefix != "" {
		in.Prefix = aws.String(prefix)
	}
	p := s3.NewListObjectVersionsPaginator(s.client, in)

	var out []s3snap.ObjectInfo
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing %s/%s: %w", bucket, prefix, err)
		}
		for _, v := range page.Versions {
			if !aws.ToBool(v.IsLatest) {
				continue
			}
			out = append(out, s3snap.ObjectInfo{
				Key:       aws.ToString(v.Key),
				VersionID: aws.ToString(v.VersionId),
				Size:      aws.ToInt64(v.Size),
				ETag:      aws.ToString(v.ETag),
			})
		}
	}
	return out, nil
}

// CopyObject server-side-copies src to dstBucket/dstKey.
//
// Note: a single CopyObject is limited to 5 GiB. Registry blobs are far smaller,
// so this is fine in practice; copying larger objects would require a multipart
// copy, which is left for a later iteration.
func (s *Store) CopyObject(ctx context.Context, src s3snap.ObjectRef, dstBucket, dstKey string) error {
	copySource := url.PathEscape(src.Bucket + "/" + src.Key)
	if src.VersionID != "" && src.VersionID != "null" {
		copySource += "?versionId=" + src.VersionID
	}
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(dstBucket),
		Key:        aws.String(dstKey),
		CopySource: aws.String(copySource),
	})
	if err != nil {
		return fmt.Errorf("copying %s/%s -> %s/%s: %w", src.Bucket, src.Key, dstBucket, dstKey, err)
	}
	return nil
}

// GetObject reads the referenced object (a specific version when set).
func (s *Store) GetObject(ctx context.Context, ref s3snap.ObjectRef) ([]byte, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key)}
	if ref.VersionID != "" {
		in.VersionId = aws.String(ref.VersionID)
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("getting %s/%s: %w", ref.Bucket, ref.Key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s/%s: %w", ref.Bucket, ref.Key, err)
	}
	return data, nil
}

// PutObject writes data to bucket/key.
func (s *Store) PutObject(ctx context.Context, bucket, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("putting %s/%s: %w", bucket, key, err)
	}
	return nil
}

// DeleteObject deletes the current version of bucket/key.
func (s *Store) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("deleting %s/%s: %w", bucket, key, err)
	}
	return nil
}

// HeadObject returns metadata for bucket/key; ok is false when it does not exist.
func (s *Store) HeadObject(ctx context.Context, bucket, key string) (s3snap.ObjectInfo, bool, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return s3snap.ObjectInfo{}, false, nil
		}
		return s3snap.ObjectInfo{}, false, fmt.Errorf("heading %s/%s: %w", bucket, key, err)
	}
	return s3snap.ObjectInfo{
		Key:       key,
		VersionID: aws.ToString(out.VersionId),
		Size:      aws.ToInt64(out.ContentLength),
		ETag:      aws.ToString(out.ETag),
	}, true, nil
}

// isNotFound reports whether err is an S3 "object does not exist" error.
func isNotFound(err error) bool {
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey":
			return true
		}
	}
	return false
}

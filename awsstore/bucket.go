package awsstore

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// BucketOptions configures EnsureVersionedBucket.
type BucketOptions struct {
	// Region the bucket lives in. Required to create it in the right place.
	Region string
	// ExpireNoncurrentDays, when > 0, installs a lifecycle rule that expires
	// non-current object versions after this many days. 0 disables the rule.
	ExpireNoncurrentDays int32
}

// EnsureVersionedBucket makes sure the durable snapshot bucket exists with
// versioning enabled (and, optionally, a lifecycle rule for old versions). It is
// idempotent and safe to call repeatedly. This mirrors the shell prototype's
// 00-setup step and is typically run once, out of band from taking snapshots.
func EnsureVersionedBucket(ctx context.Context, client *s3.Client, bucket string, opts BucketOptions) error {
	exists, err := bucketExists(ctx, client, bucket)
	if err != nil {
		return err
	}
	if !exists {
		if err := createBucket(ctx, client, bucket, opts.Region); err != nil {
			return err
		}
		if err := blockPublicAccess(ctx, client, bucket); err != nil {
			return err
		}
	}

	if _, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucket),
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	}); err != nil {
		return fmt.Errorf("enabling versioning on %s: %w", bucket, err)
	}

	if opts.ExpireNoncurrentDays > 0 {
		if err := putNoncurrentExpiry(ctx, client, bucket, opts.ExpireNoncurrentDays); err != nil {
			return err
		}
	}
	return nil
}

func bucketExists(ctx context.Context, client *s3.Client, bucket string) (bool, error) {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("checking bucket %s: %w", bucket, err)
}

func createBucket(ctx context.Context, client *s3.Client, bucket, region string) error {
	in := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	// us-east-1 must not carry a LocationConstraint; every other region must.
	if region != "" && region != "us-east-1" {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}
	if _, err := client.CreateBucket(ctx, in); err != nil {
		return fmt.Errorf("creating bucket %s: %w", bucket, err)
	}
	return nil
}

func blockPublicAccess(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("blocking public access on %s: %w", bucket, err)
	}
	return nil
}

func putNoncurrentExpiry(ctx context.Context, client *s3.Client, bucket string, days int32) error {
	_, err := client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucket),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{
			Rules: []types.LifecycleRule{{
				ID:                          aws.String("expire-noncurrent-snapshots"),
				Status:                      types.ExpirationStatusEnabled,
				Filter:                      &types.LifecycleRuleFilter{Prefix: aws.String("")},
				NoncurrentVersionExpiration: &types.NoncurrentVersionExpiration{NoncurrentDays: aws.Int32(days)},
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("setting lifecycle on %s: %w", bucket, err)
	}
	return nil
}

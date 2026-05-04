package s3

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
)

type store struct {
	client *s3.Client
	up     *manager.Uploader
	down   *manager.Downloader
	bucket string
}

// NewStore creates a new S3-compatible snapshot store.
func NewStore(ctx context.Context, cfg Config) (ports.SnapshotStore, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	awscfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKey, cfg.SecretKey, "",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("s3: load aws config: %w", err)
	}

	client := s3.NewFromConfig(awscfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		if cfg.UsePathStyle {
			o.UsePathStyle = true
		}
	})

	return &store{
		client: client,
		up:     manager.NewUploader(client, func(u *manager.Uploader) { u.PartSize = 64 * 1024 * 1024 }),
		down:   manager.NewDownloader(client),
		bucket: cfg.Bucket,
	}, nil
}

func (s *store) UploadFiles(ctx context.Context, files []ports.SnapshotFile) (int64, error) {
	logger := pkglog.FromContext(ctx)
	var total int64

	for _, f := range files {
		fi, err := os.Open(f.LocalPath)
		if err != nil {
			return total, fmt.Errorf("s3: open %s: %w", f.LocalPath, err)
		}

		_, err = s.up.Upload(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(f.Key),
			Body:   fi,
		})
		if err != nil {
			fi.Close()
			return total, fmt.Errorf("s3: upload %s: %w", f.Key, err)
		}
		fi.Close()

		logger.DebugContext(ctx, "s3: uploaded file", "key", f.Key)

		if stat, serr := os.Stat(f.LocalPath); serr == nil {
			total += stat.Size()
		}
	}

	return total, nil
}

func (s *store) ObjectExists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey" {
			return false, nil
		}
	}

	return false, fmt.Errorf("s3: head object %s: %w", key, err)
}

func (s *store) DownloadFiles(ctx context.Context, files []ports.SnapshotFile) error {
	for _, f := range files {
		dir := filepath.Dir(f.LocalPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("s3: create dir for %s: %w", f.LocalPath, err)
		}

		fi, err := os.Create(f.LocalPath)
		if err != nil {
			return fmt.Errorf("s3: create %s: %w", f.LocalPath, err)
		}

		_, err = s.down.Download(ctx, fi, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(f.Key),
		})
		fi.Close()
		if err != nil {
			os.Remove(f.LocalPath)
			return fmt.Errorf("s3: download %s: %w", f.Key, err)
		}
	}
	return nil
}

func (s *store) DeletePrefix(ctx context.Context, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("s3: list prefix %s: %w", prefix, err)
		}

		if len(page.Contents) == 0 {
			continue
		}

		objects := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, obj := range page.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: obj.Key})
		}

		_, err = s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: objects},
		})
		if err != nil {
			return fmt.Errorf("s3: delete prefix %s: %w", prefix, err)
		}
	}

	return nil
}

func (s *store) EnsureBucket(ctx context.Context) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		var bae *types.BucketAlreadyExists
		var baoby *types.BucketAlreadyOwnedByYou
		if errors.As(err, &bae) || errors.As(err, &baoby) {
			return nil
		}
		return fmt.Errorf("s3: create bucket %s: %w", s.bucket, err)
	}
	return nil
}

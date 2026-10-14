package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"telesphorepo/internal/config"
)

type s3Store struct {
	c        *s3.Client
	ps       *s3.PresignClient
	bucket   string
	prefix   string // "" or "dir/"
	redirect bool
}

// NewS3 returns an S3-compatible Store (R2, SeaweedFS) from cfg.S3*; keys are prefixed with
// cfg.S3Prefix. PresignGet returns ok=false when !cfg.S3Redirect.
func NewS3(ctx context.Context, cfg *config.Config) (Store, error) {
	ac, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, "")),
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)
	if err != nil {
		return nil, err
	}
	c := s3.NewFromConfig(ac, func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
		o.UsePathStyle = cfg.S3PathStyle
	})
	s := &s3Store{c: c, ps: s3.NewPresignClient(c), bucket: cfg.S3Bucket, redirect: cfg.S3Redirect}
	if p := strings.Trim(cfg.S3Prefix, "/"); p != "" {
		s.prefix = p + "/"
	}
	return s, nil
}

func (s *s3Store) key(k string) *string { return aws.String(s.prefix + k) }

func isNotFound(err error) bool {
	var nf *types.NotFound
	var nsk *types.NoSuchKey
	if errors.As(err, &nf) || errors.As(err, &nsk) {
		return true
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NotFound", "NoSuchKey", "404":
			return true
		}
	}
	var he interface{ HTTPStatusCode() int }
	return errors.As(err, &he) && he.HTTPStatusCode() == http.StatusNotFound
}

func mapS3(err error) error {
	if err != nil && isNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (s *s3Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: s.key(key), Body: r, ContentLength: aws.Int64(size)}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	_, err := s.c.PutObject(ctx, in)
	return err
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	o, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: s.key(key)})
	if err != nil {
		return nil, ObjectInfo{}, mapS3(err)
	}
	return o.Body, ObjectInfo{Key: key, Size: aws.ToInt64(o.ContentLength), ModTime: aws.ToTime(o.LastModified)}, nil
}

func (s *s3Store) Head(ctx context.Context, key string) (ObjectInfo, error) {
	o, err := s.c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: s.key(key)})
	if err != nil {
		return ObjectInfo{}, mapS3(err)
	}
	return ObjectInfo{Key: key, Size: aws.ToInt64(o.ContentLength), ModTime: aws.ToTime(o.LastModified)}, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	_, err := s.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: s.key(key)})
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

func (s *s3Store) List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error) {
	in := &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(s.prefix + prefix)}
	if after != "" {
		in.StartAfter = aws.String(s.prefix + after)
	}
	var out []ObjectInfo
	for limit <= 0 || len(out) < limit {
		if limit > 0 {
			in.MaxKeys = aws.Int32(int32(min(limit-len(out), 1000)))
		}
		res, err := s.c.ListObjectsV2(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, o := range res.Contents {
			out = append(out, ObjectInfo{
				Key:     strings.TrimPrefix(aws.ToString(o.Key), s.prefix),
				Size:    aws.ToInt64(o.Size),
				ModTime: aws.ToTime(o.LastModified),
			})
		}
		if !aws.ToBool(res.IsTruncated) || res.NextContinuationToken == nil {
			break
		}
		in.ContinuationToken = res.NextContinuationToken
		in.StartAfter = nil
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *s3Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	if !s.redirect {
		return "", false, nil
	}
	r, err := s.ps.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: s.key(key)}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", false, err
	}
	return r.URL, true, nil
}

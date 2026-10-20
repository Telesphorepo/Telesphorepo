package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type s3Store struct {
	c        *s3.Client
	ps       *s3.PresignClient
	bucket   string
	prefix   string // "" or "dir/"
	redirect bool
}

// NewS3 returns an S3 Store (AWS, or an S3-compatible store such as R2 or SeaweedFS at
// cfg.S3Endpoint) from cfg.S3*; keys are prefixed with cfg.S3Prefix. Without an endpoint the
// client uses the AWS endpoint of cfg.S3Region; with an endpoint and no region it signs for the
// region "auto". PresignGet returns ok=false when !cfg.S3Redirect.
func NewS3(ctx context.Context, cfg Config) (Store, error) {
	region := cfg.S3Region
	if region == "" && cfg.S3Endpoint != "" {
		region = "auto"
	}
	ac, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
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

func (s *s3Store) GetRange(ctx context.Context, key string, off, n int64) (io.ReadCloser, ObjectInfo, error) {
	o, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: s.key(key),
		Range: aws.String(fmt.Sprintf("bytes=%d-%d", off, off+n-1))})
	if err != nil {
		return nil, ObjectInfo{}, mapS3(err)
	}
	size := aws.ToInt64(o.ContentLength)
	if cr := aws.ToString(o.ContentRange); cr != "" {
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if total, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				size = total
			}
		}
	}
	return o.Body, ObjectInfo{Key: key, Size: size, ModTime: aws.ToTime(o.LastModified)}, nil
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

// deleteBatch is the most keys one DeleteObjects call takes.
const deleteBatch = 1000

func (s *s3Store) DeleteMany(ctx context.Context, keys []string) error {
	for chunk := range slices.Chunk(keys, deleteBatch) {
		objs := make([]types.ObjectIdentifier, len(chunk))
		for i, k := range chunk {
			objs[i] = types.ObjectIdentifier{Key: s.key(k)}
		}
		res, err := s.c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &s.bucket,
			Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)}})
		if err != nil {
			return err
		}
		for _, e := range res.Errors {
			switch aws.ToString(e.Code) {
			case "NoSuchKey", "NotFound":
				continue
			}
			return fmt.Errorf("delete %s: %s: %s", strings.TrimPrefix(aws.ToString(e.Key), s.prefix),
				aws.ToString(e.Code), aws.ToString(e.Message))
		}
	}
	return nil
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

func (s *s3Store) PresignGet(ctx context.Context, key string, ttl time.Duration, resp Response) (string, bool, error) {
	if !s.redirect {
		return "", false, nil
	}
	in := &s3.GetObjectInput{Bucket: &s.bucket, Key: s.key(key)}
	if resp.ContentType != "" {
		in.ResponseContentType = aws.String(resp.ContentType)
	}
	if resp.ContentDisposition != "" {
		in.ResponseContentDisposition = aws.String(resp.ContentDisposition)
	}
	r, err := s.ps.PresignGetObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", false, err
	}
	return r.URL, true, nil
}

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
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
//
// The HTTP client keeps up to maxIdleConnsPerHost connections to the store (the storage move
// copies 16 blobs at once, a multipart upload sends partWorkers parts at once), and a connection
// that reads or writes nothing for ioTimeout fails its request, which the retryer then tries
// again. The retryer has no retry quota: during an outage of the store each call makes its own
// attempts with a backoff, instead of every call failing at once when a shared quota is spent.
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
		awsconfig.WithHTTPClient(s3HTTPClient()),
		awsconfig.WithRetryer(func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) { o.RateLimiter = ratelimit.None })
		}),
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

const (
	// maxIdleConnsPerHost is how many idle connections to the store the client keeps.
	maxIdleConnsPerHost = 64
	// ioTimeout is how long a connection may go without reading or writing a byte. It resets on
	// every byte, so a slow transfer that progresses never fails on it.
	ioTimeout = time.Minute
)

// s3HTTPClient is the HTTP client of the S3 store: the SDK's client with a larger pool, a read
// timeout (the SDK's, which fails with a retryable timeout error) and a write timeout.
func s3HTTPClient() aws.HTTPClient {
	return awshttp.NewBuildableClient().
		WithTransportOptions(func(tr *http.Transport) {
			tr.MaxIdleConns = 4 * maxIdleConnsPerHost
			tr.MaxIdleConnsPerHost = maxIdleConnsPerHost
			d := &net.Dialer{Timeout: awshttp.DefaultDialConnectTimeout, KeepAlive: awshttp.DefaultDialKeepAliveTimeout}
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				c, err := d.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &writeDeadlineConn{Conn: c}, nil
			}
		}).
		WithReadTimeout(ioTimeout).
		Freeze()
}

// writeDeadlineConn fails a write that sends nothing for ioTimeout (a peer that stopped reading).
type writeDeadlineConn struct{ net.Conn }

func (c *writeDeadlineConn) Write(p []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
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

// The multipart upload of a large object.
const (
	// multipartThreshold is the size from which Put uploads in parts. A single put takes at most
	// 5 GiB (R2: 4.995 GiB) and goes over one connection.
	multipartThreshold = 64 << 20
	// minPartSize is the size of each part but the last; a larger object gets larger parts, so
	// it fits in maxParts.
	minPartSize = 16 << 20
	maxParts    = 10000
	// partWorkers is how many parts one upload sends at once.
	partWorkers = 8
)

// Put writes the object with one PutObject, or, from multipartThreshold, as a multipart upload.
func (s *s3Store) Put(ctx context.Context, key string, r io.ReaderAt, size int64, contentType string) error {
	if size >= multipartThreshold {
		return s.putMultipart(ctx, key, r, size, contentType)
	}
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: s.key(key), Body: io.NewSectionReader(r, 0, size),
		ContentLength: aws.Int64(size)}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	_, err := s.c.PutObject(ctx, in)
	return err
}

// partSize is the part size of an object of size bytes: minPartSize, or the next MiB above
// size/maxParts. Every part but the last has this size (R2 requires parts of equal size).
func partSize(size int64) int64 {
	ps := int64(minPartSize)
	if n := (size + maxParts - 1) / maxParts; n > ps {
		ps = (n + 1<<20 - 1) &^ (1<<20 - 1)
	}
	return ps
}

// putMultipart uploads the object in parts, partWorkers at once, each part read from r through
// its own section reader. On an error it aborts the upload, so the store keeps no parts.
func (s *s3Store) putMultipart(ctx context.Context, key string, r io.ReaderAt, size int64, contentType string) error {
	in := &s3.CreateMultipartUploadInput{Bucket: &s.bucket, Key: s.key(key)}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	up, err := s.c.CreateMultipartUpload(ctx, in)
	if err != nil {
		return err
	}
	ps := partSize(size)
	n := int((size + ps - 1) / ps)
	parts := make([]types.CompletedPart, n)

	pctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(partWorkers, n) {
		wg.Go(func() {
			for i := range next {
				off := int64(i) * ps
				body := io.NewSectionReader(r, off, min(ps, size-off))
				num := aws.Int32(int32(i + 1))
				res, err := s.c.UploadPart(pctx, &s3.UploadPartInput{Bucket: &s.bucket, Key: s.key(key),
					UploadId: up.UploadId, PartNumber: num, Body: body, ContentLength: aws.Int64(body.Size())})
				if err != nil {
					stop(fmt.Errorf("upload part %d of %d: %w", i+1, n, err))
					continue
				}
				parts[i] = types.CompletedPart{ETag: res.ETag, PartNumber: num}
			}
		})
	}
feed:
	for i := range n {
		select {
		case next <- i:
		case <-pctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	err = context.Cause(pctx)
	if err == nil {
		_, err = s.c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: &s.bucket, Key: s.key(key),
			UploadId: up.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}})
	}
	if err != nil {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		s.c.AbortMultipartUpload(actx, &s3.AbortMultipartUploadInput{Bucket: &s.bucket, Key: s.key(key), UploadId: up.UploadId})
		return err
	}
	return nil
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

// DeleteMany sends one DeleteObjects per deleteBatch keys. A failed call leaves its keys and
// the keys after it; a key that the answer lists with an error stays too. Both go into the
// *DeleteError.
func (s *s3Store) DeleteMany(ctx context.Context, keys []string) error {
	var de DeleteError
	for start := 0; start < len(keys); start += deleteBatch {
		chunk := keys[start:min(start+deleteBatch, len(keys))]
		objs := make([]types.ObjectIdentifier, len(chunk))
		for i, k := range chunk {
			objs[i] = types.ObjectIdentifier{Key: s.key(k)}
		}
		res, err := s.c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &s.bucket,
			Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)}})
		if err != nil {
			de.add(keys[start:], err)
			break
		}
		for _, e := range res.Errors {
			switch aws.ToString(e.Code) {
			case "NoSuchKey", "NotFound":
				continue
			}
			k := strings.TrimPrefix(aws.ToString(e.Key), s.prefix)
			de.add([]string{k}, fmt.Errorf("delete %s: %s: %s", k, aws.ToString(e.Code), aws.ToString(e.Message)))
		}
	}
	return de.err()
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

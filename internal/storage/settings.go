package storage

import (
	"errors"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// The settings that select and configure the blob store. The storage module owns them (group
// "storage"); the kernel reads them through ConfigView.
var (
	Backend = settings.String(&settings.Def{Key: "storage", Default: "fs", Label: "Storage backend",
		Help:    "With fs, the server stores blobs on the data volume. With s3, it uses any S3-compatible store (AWS, Cloudflare R2, SeaweedFS). When the location changes, the server moves the existing blobs to the new store in the background.",
		Options: []string{"fs", "s3"}, Normalize: settings.Lower})
	FSRoot = settings.String(&settings.Def{Key: "fs.root", DataDefault: "blobs", Label: "Filesystem root",
		Help: "The server keeps blobs in this directory when storage is fs. The default is blobs/ in the data directory.", Normalize: settings.Trim})
	S3Endpoint = settings.String(&settings.Def{Key: "s3.endpoint", Label: "S3 endpoint",
		Help:      "The URL of the S3 API, for example https://<account>.r2.cloudflarestorage.com or http://seaweedfs:8333. Leave it empty for AWS: the server then uses the AWS endpoint of the S3 region, so the region is required.",
		Normalize: settings.Trim, Validate: settings.HTTPURL})
	S3Region = settings.String(&settings.Def{Key: "s3.region", Label: "S3 region",
		Help:      "The region of the bucket, for example us-east-1. It is required when the endpoint is empty (AWS). With an endpoint and no region, the server signs for the region auto, which Cloudflare R2 and SeaweedFS accept.",
		Normalize: settings.Trim})
	S3Bucket = settings.String(&settings.Def{Key: "s3.bucket", Label: "S3 bucket", Normalize: settings.Trim,
		Help: "The bucket that holds the blobs. Required when storage is s3."})
	S3AccessKeyID = settings.String(&settings.Def{Key: "s3.access_key_id", Label: "S3 access key ID", Normalize: settings.Trim,
		Help: "Required when storage is s3."})
	S3SecretAccessKey = settings.String(&settings.Def{Key: "s3.secret_access_key", Secret: true, Label: "S3 secret access key",
		Help: "Required when storage is s3."})
	S3Prefix = settings.String(&settings.Def{Key: "s3.prefix", Label: "S3 key prefix",
		Help: "Every object key starts with this prefix, for example telesphorepo/. Leave it empty for no prefix.",
		Normalize: func(v any) any {
			s := strings.Trim(strings.TrimSpace(v.(string)), "/")
			if s != "" {
				s = path.Clean(s)
			}
			return s
		},
		Validate: func(v any) error {
			for _, seg := range strings.Split(v.(string), "/") {
				if seg == ".." {
					return errors.New("must not contain ..")
				}
			}
			return nil
		}})
	S3PathStyle = settings.Bool(&settings.Def{Key: "s3.path_style", Default: "false", Label: "S3 path-style addressing",
		Help: "SeaweedFS and MinIO need path-style addresses. R2 and AWS do not."})
	S3Redirect = settings.Bool(&settings.Def{Key: "s3.redirect", Default: "true", Label: "Redirect downloads to S3",
		Help: "When this is on, a GET download answers 302 with a presigned URL. When it is off, the server streams the file."})
	PresignTTL = settings.Duration(&settings.Def{Key: "presign_ttl", Default: "5m", Label: "Presigned URL lifetime",
		Help: "A presigned download URL works for this time."})
)

// LocationKeys are the settings that select where the blobs are.
var LocationKeys = []string{Backend.Name(), FSRoot.Name(), S3Endpoint.Name(), S3Region.Name(), S3Bucket.Name(),
	S3AccessKeyID.Name(), S3SecretAccessKey.Name(), S3Prefix.Name(), S3PathStyle.Name(), S3Redirect.Name()}

// Defs returns the definitions of the storage settings, in display order.
func Defs() []*settings.Def {
	return []*settings.Def{Backend.Def(), FSRoot.Def(), S3Endpoint.Def(), S3Region.Def(), S3Bucket.Def(),
		S3AccessKeyID.Def(), S3SecretAccessKey.Def(), S3Prefix.Def(), S3PathStyle.Def(), S3Redirect.Def(), PresignTTL.Def()}
}

// Config is the configuration of a blob store. A storage move seals it as JSON, so it holds the
// S3 secret: never log it.
type Config struct {
	Backend           string        `json:"storage"` // "fs" or "s3"
	FSRoot            string        `json:"fs_root"`
	S3Endpoint        string        `json:"s3_endpoint"` // "" for AWS
	S3Region          string        `json:"s3_region"`
	S3Bucket          string        `json:"s3_bucket"`
	S3AccessKeyID     string        `json:"s3_access_key_id"`
	S3SecretAccessKey string        `json:"s3_secret_access_key"`
	S3Prefix          string        `json:"s3_prefix"`
	S3PathStyle       bool          `json:"s3_path_style"`
	S3Redirect        bool          `json:"s3_redirect"`
	PresignTTL        time.Duration `json:"-"`
}

// ConfigView is the configuration of the blob store in a settings snapshot.
var ConfigView = settings.NewView(func(s *settings.Snapshot) Config {
	return Config{
		Backend: Backend.Get(s), FSRoot: FSRoot.Get(s),
		S3Endpoint: S3Endpoint.Get(s), S3Region: S3Region.Get(s), S3Bucket: S3Bucket.Get(s),
		S3AccessKeyID: S3AccessKeyID.Get(s), S3SecretAccessKey: S3SecretAccessKey.Get(s), S3Prefix: S3Prefix.Get(s),
		S3PathStyle: S3PathStyle.Get(s), S3Redirect: S3Redirect.Get(s), PresignTTL: PresignTTL.Get(s),
	}
})

// Validate checks the storage settings as a whole: an s3 store needs a bucket and credentials,
// and a region when the endpoint is empty (AWS).
func Validate(d *settings.Draft) {
	c := ConfigView.Get(d.Next)
	if c.Backend != "s3" {
		return
	}
	for _, k := range []settings.Key[string]{S3Bucket, S3AccessKeyID, S3SecretAccessKey} {
		if k.Get(d.Next) == "" {
			msg := "required when storage is s3"
			if why := d.Broken(k.Name()); why != "" {
				msg += " (the stored value cannot be read: " + why + ")"
			}
			d.Fail(k.Name(), msg)
		}
	}
	if c.S3Endpoint == "" && c.S3Region == "" {
		d.Fail(S3Region.Name(), "required when the S3 endpoint is empty (AWS)")
	}
}

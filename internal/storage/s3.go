package storage

import (
	"context"

	"telesphorepo/internal/config"
)

// NewS3 returns an S3-compatible Store (R2, SeaweedFS) from cfg.S3*; keys are prefixed with
// cfg.S3Prefix. PresignGet returns ok=false when !cfg.S3Redirect.
func NewS3(ctx context.Context, cfg *config.Config) (Store, error) { panic("TODO") }

package backup

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// Offline returns the source of a backup from the command line: the database of dataDir opened
// read-only (a server may run on it), its storage settings (the TR_* environment, else the stored
// rows, else the defaults) and, for fs, the files of its store. Close the returned database.
func Offline(dataDir, version string) (Source, *sql.DB, error) {
	file := config.JoinDataDir(dataDir, DBFile)
	d, err := db.OpenReadOnly(file)
	if err != nil {
		return Source{}, nil, err
	}
	ctx := context.Background()
	cfg := storage.Config{}
	for _, k := range []struct {
		key settings.Key[string]
		out *string
		def string
	}{
		{storage.Backend, &cfg.Backend, "fs"},
		{storage.S3Endpoint, &cfg.S3Endpoint, ""},
		{storage.S3Bucket, &cfg.S3Bucket, ""},
		{storage.S3Prefix, &cfg.S3Prefix, ""},
	} {
		v := strings.TrimSpace(os.Getenv(k.key.Def().Env()))
		if v == "" {
			if v, err = storedString(ctx, d, k.key.Name()); err != nil {
				d.Close()
				return Source{}, nil, err
			}
		}
		if v == "" {
			v = k.def
		}
		*k.out = v
	}
	cfg.Backend = strings.ToLower(cfg.Backend)
	if cfg.FSRoot, err = fsRoot(ctx, d, dataDir); err != nil {
		d.Close()
		return Source{}, nil, err
	}
	src := Source{
		DB: d, DataDir: dataDir, Version: version, Storage: cfg,
		MasterKeyEnv: os.Getenv("TR_MASTER_KEY") != "",
		Get: func(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
			f, err := os.Open(filepath.Join(cfg.FSRoot, filepath.FromSlash(key)))
			if os.IsNotExist(err) {
				return nil, storage.ObjectInfo{}, storage.ErrNotFound
			}
			if err != nil {
				return nil, storage.ObjectInfo{}, err
			}
			return f, storage.ObjectInfo{Key: key}, nil
		},
	}
	return src, d, nil
}

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/backup"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/server"
)

// backupCmd writes one backup of the data dir into the folder of its argument. It reads the
// database read-only, so a server may run on the same data dir.
func backupCmd(args []string) error {
	cmd := find(commands, "backup")
	var dir string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "-"):
			return &usageError{fmt.Sprintf("unknown flag %q", a), cmd}
		case dir == "":
			dir = a
		default:
			return &usageError{fmt.Sprintf("unexpected argument %q", a), cmd}
		}
	}
	if dir == "" {
		return &usageError{"missing backup folder", cmd}
	}
	dataDir, _ := config.CLIDataDir()
	src, d, err := backup.Offline(dataDir, version)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no database in %s (set TR_DATA_DIR to the data folder of the server)", dataDir)
	}
	if err != nil {
		return err
	}
	defer d.Close()
	ctx, stop := signalContext()
	defer stop()
	res, err := backup.Run(ctx, dir, src)
	if err != nil {
		return err
	}
	m := res.Manifest
	fmt.Printf("Backed up %s into %s\n", dataDir, res.Folder)
	fmt.Printf("  database    %s, %s\n", backup.DBFile, sizeText(m.Database.Size))
	switch {
	case m.MasterKeyIncluded:
		fmt.Printf("  master key  %s\n", config.MasterKeyFileName)
	case m.MasterKeySource == "env":
		fmt.Println("  master key  not included: it is in TR_MASTER_KEY; keep it with the backup")
	}
	if m.BlobsIncluded {
		fmt.Printf("  files       %d in %s (%d copied now, %s)\n", m.Counts.Blobs, filepath.Join(dir, backup.BlobsDir), res.Copied, sizeText(m.Counts.BlobBytes))
	} else {
		fmt.Printf("  files       not included: they stay in the S3 bucket %s; back up the bucket too\n", s3Text(m.S3))
	}
	return nil
}

// restoreCmd restores a backup folder into an empty data dir.
func restoreCmd(args []string) error {
	cmd := find(commands, "restore")
	var folder, dataDir string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--data-dir" || a == "-data-dir":
			if i+1 >= len(args) {
				return &usageError{"--data-dir needs a folder", cmd}
			}
			i++
			dataDir = args[i]
		case strings.HasPrefix(a, "--data-dir="):
			dataDir = strings.TrimPrefix(a, "--data-dir=")
		case strings.HasPrefix(a, "-"):
			return &usageError{fmt.Sprintf("unknown flag %q", a), cmd}
		case folder == "":
			folder = a
		default:
			return &usageError{fmt.Sprintf("unexpected argument %q", a), cmd}
		}
	}
	if folder == "" {
		return &usageError{"missing backup folder", cmd}
	}
	if dataDir == "" {
		boot, err := config.LoadBoot(nil)
		if err != nil {
			return err
		}
		dataDir = boot.DataDir
	}
	ctx, stop := signalContext()
	defer stop()
	r, err := backup.Restore(ctx, folder, dataDir)
	if err != nil {
		return err
	}
	m := r.Manifest
	fmt.Printf("Restored the backup %s (server %s, %s) into %s\n", r.Name, m.Version, m.CreatedAt.Format("2006-01-02 15:04:05 UTC"), dataDir)
	fmt.Printf("  database    %s, %s, SHA-256 checked\n", backup.DBFile, sizeText(m.Database.Size))
	if m.MasterKeyIncluded {
		fmt.Printf("  master key  %s\n", config.MasterKeyFileName)
	} else {
		fmt.Println("  master key  not in the backup: start the server with the TR_MASTER_KEY it had")
	}
	if m.BlobsIncluded {
		fmt.Printf("  files       %d copied into %s (%s)\n", r.Blobs, r.FSRoot, sizeText(m.Counts.BlobBytes))
	} else {
		fmt.Printf("  files       not in the backup: the server reads them from the S3 bucket %s, which must still hold them\n", s3Text(m.S3))
	}
	if changed := schemaChanges(m.Schema); len(changed) > 0 {
		fmt.Printf("This server has another database schema (%s) than the backup; run the server version %s on it.\n",
			strings.Join(changed, ", "), m.Version)
	}
	fmt.Printf("Start the server with TR_DATA_DIR=%s.\n", dataDir)
	return nil
}

// schemaChanges names the schema components whose fingerprint in schema differs from this
// server's.
func schemaChanges(schema map[string]string) []string {
	comps, err := server.Components()
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range comps {
		if fp, ok := schema[c.Name]; !ok || fp != db.Fingerprint(c.Schema) {
			out = append(out, c.Name)
		}
	}
	return out
}

func s3Text(l *backup.S3Location) string {
	if l == nil {
		return "of the server"
	}
	s := l.Bucket
	if l.Prefix != "" {
		s += "/" + l.Prefix
	}
	if l.Endpoint != "" {
		s += " at " + l.Endpoint
	}
	return s
}

// sizeText puts a byte count in binary units, e.g. "3.2 MiB".
func sizeText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/server"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

func keygenCmd(args []string) error {
	cmd := find(commands, "keygen")
	var file string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "-") && a != "-":
			return &usageError{fmt.Sprintf("unknown flag %q", a), cmd}
		case file == "":
			file = a
		default:
			return &usageError{fmt.Sprintf("unexpected argument %q", a), cmd}
		}
	}
	if file == "" {
		file = config.JoinDataDir(config.DataDirFromEnv(), config.SigningKeyFileName)
	}
	if dir := filepath.Dir(file); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	fp, err := signing.GenerateFile(file, server.DefaultKeyName, server.DefaultKeyEmail)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists; refusing to overwrite it (choose another file: telesphorepo keygen new-key.asc)", file)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Wrote signing key %s\nfingerprint: %s\n", file, fp)
	return nil
}

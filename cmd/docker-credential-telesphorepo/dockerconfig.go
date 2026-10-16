package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// helperName is the name that Docker's credHelpers use: Docker runs docker-credential-<name>.
const helperName = "telesphorepo"

// dockerConfigPath is $DOCKER_CONFIG/config.json, else ~/.docker/config.json.
func dockerConfigPath() (string, error) {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot find the home folder: %w", err)
	}
	return filepath.Join(home, ".docker", "config.json"), nil
}

// addCredHelpers sets "credHelpers": {"<host>": "telesphorepo"} for each host in the Docker
// config file and keeps every other key. It returns the path of the file.
func addCredHelpers(hosts []string) (string, error) {
	path, err := dockerConfigPath()
	if err != nil {
		return "", err
	}
	cfg := map[string]json.RawMessage{}
	mode := fs.FileMode(0o600)
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	default:
		if err := json.Unmarshal(b, &cfg); err != nil {
			return "", fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
	}
	helpers := map[string]string{}
	if raw, ok := cfg["credHelpers"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &helpers); err != nil {
			return "", fmt.Errorf("credHelpers in %s is not an object of strings: %w", path, err)
		}
	}
	for _, h := range hosts {
		helpers[h] = helperName
	}
	raw, err := json.Marshal(helpers)
	if err != nil {
		return "", err
	}
	cfg["credHelpers"] = raw
	out, err := json.MarshalIndent(cfg, "", "\t")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, append(out, '\n'), mode); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("cannot write %s: %w", path, err)
	}
	return path, nil
}

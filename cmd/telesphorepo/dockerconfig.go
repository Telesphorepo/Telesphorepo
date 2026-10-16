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

// dockerConfig is the Docker config file: every key as it is, and credHelpers parsed.
type dockerConfig struct {
	path    string
	keys    map[string]json.RawMessage
	helpers map[string]string
	mode    fs.FileMode
	exists  bool
}

// readDockerConfig reads the Docker config file. A file that does not exist is empty.
func readDockerConfig() (*dockerConfig, error) {
	path, err := dockerConfigPath()
	if err != nil {
		return nil, err
	}
	cfg := &dockerConfig{path: path, keys: map[string]json.RawMessage{}, helpers: map[string]string{}, mode: 0o600}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cfg, nil
	case err != nil:
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	cfg.exists = true
	if err := json.Unmarshal(b, &cfg.keys); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if cfg.keys == nil {
		cfg.keys = map[string]json.RawMessage{}
	}
	if st, err := os.Stat(path); err == nil {
		cfg.mode = st.Mode().Perm()
	}
	if raw, ok := cfg.keys["credHelpers"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &cfg.helpers); err != nil {
			return nil, fmt.Errorf("credHelpers in %s is not an object of strings: %w", path, err)
		}
	}
	return cfg, nil
}

// write saves the config with its credHelpers and keeps every other key.
func (cfg *dockerConfig) write() error {
	if len(cfg.helpers) == 0 {
		delete(cfg.keys, "credHelpers")
	} else {
		raw, err := json.Marshal(cfg.helpers)
		if err != nil {
			return err
		}
		cfg.keys["credHelpers"] = raw
	}
	out, err := json.MarshalIndent(cfg.keys, "", "\t")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.path), 0o700); err != nil {
		return err
	}
	tmp := cfg.path + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, append(out, '\n'), cfg.mode); err != nil {
		return fmt.Errorf("cannot write %s: %w", cfg.path, err)
	}
	if err := os.Rename(tmp, cfg.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cannot write %s: %w", cfg.path, err)
	}
	return nil
}

// addCredHelpers sets "credHelpers": {"<host>": "telesphorepo"} for each host in the Docker
// config file and keeps every other key. It returns the path of the file.
func addCredHelpers(hosts []string) (string, error) {
	cfg, err := readDockerConfig()
	if err != nil {
		return "", err
	}
	for _, h := range hosts {
		cfg.helpers[h] = helperName
	}
	return cfg.path, cfg.write()
}

// removeCredHelpers removes the entry "<host>": "telesphorepo" of each host from credHelpers and
// keeps every other entry and key. It returns the path of the file and the hosts it removed.
func removeCredHelpers(hosts []string) (string, []string, error) {
	cfg, err := readDockerConfig()
	if err != nil || !cfg.exists {
		return "", nil, err
	}
	var removed []string
	for _, h := range hosts {
		if cfg.helpers[h] == helperName {
			delete(cfg.helpers, h)
			removed = append(removed, h)
		}
	}
	if len(removed) == 0 {
		return cfg.path, nil, nil
	}
	return cfg.path, removed, cfg.write()
}

// currentCredHelpers returns the credHelpers of the Docker config file (none when it cannot be
// read).
func currentCredHelpers() map[string]string {
	cfg, err := readDockerConfig()
	if err != nil {
		return map[string]string{}
	}
	return cfg.helpers
}

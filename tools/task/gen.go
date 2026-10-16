package main

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
)

// gen runs sqlc for every internal/**/sqlc.yaml: the kernel and each module with a
// schema own their own config, queries and generated dbq package.
func gen(ctx context.Context) error {
	var configs []string
	err := filepath.WalkDir("internal", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "sqlc.yaml" {
			configs = append(configs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(configs) == 0 {
		return fmt.Errorf("no sqlc.yaml under internal/")
	}
	for _, c := range configs {
		step("sqlc generate %s", filepath.ToSlash(c))
		if err := runStep(ctx, ".", nil, "go", "tool", "sqlc", "generate", "-f", c); err != nil {
			return fmt.Errorf("sqlc failed for %s: %w", filepath.ToSlash(c), err)
		}
	}
	out.line(green("✓ ") + fmt.Sprintf("generated %d sqlc package(s)", len(configs)))
	return nil
}

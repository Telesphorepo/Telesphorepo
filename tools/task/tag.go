package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/changelog"
)

// tag makes a release: it writes the version into each "dev" entry of changes.toml, commits the
// change log, and creates the annotated tag v<version>. The tagged commit then holds the stamped
// log, so the release workflow and every image build embed the same entries. It needs a clean
// work tree and pushes nothing.
func tag(ctx context.Context, version string) error {
	if _, err := changelog.ParseVersion(version); err != nil {
		return usageErr(fmt.Sprintf("%q is not a release version (go tool task tag 1.2.3)", version))
	}
	version = "v" + strings.TrimPrefix(version, "v")
	if out, err := git(ctx, "status", "--porcelain"); err != nil {
		return err
	} else if out != "" {
		return fmt.Errorf("the work tree has changes; commit or stash them first:\n%s", out)
	}
	if _, err := git(ctx, "rev-parse", "--verify", "--quiet", "refs/tags/"+version); err == nil {
		return fmt.Errorf("the tag %s exists", version)
	}
	n, err := stampChanges(version)
	if err != nil {
		return err
	}
	if n > 0 {
		if _, err := git(ctx, "commit", "-m", "Release "+version, "--", changesFile); err != nil {
			return err
		}
	}
	if _, err := git(ctx, "tag", "-a", version, "-m", "Release "+version); err != nil {
		return err
	}
	out.line(green("✓ ") + fmt.Sprintf("tagged %s; push it with: git push origin HEAD %s", bold(version), version))
	return nil
}

// git runs git with args and returns its trimmed output.
func git(ctx context.Context, args ...string) (string, error) {
	b, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	s := strings.TrimSpace(string(b))
	if err != nil {
		return s, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

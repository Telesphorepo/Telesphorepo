package main

import (
	"errors"
	"os/exec"
)

// runCargo runs cargo publish to the repo as the registry telesphorepo: its sparse index and the
// token provider cargo:token come from --config, and the token from
// CARGO_REGISTRIES_TELESPHOREPO_TOKEN in the env of cargo, so no file holds it. Cargo packs and
// verifies the crate itself; the session also reads the repo, so the verify build resolves the
// repo's own crates.
func runCargo(j *job) error {
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		return errors.New("cargo is not on the PATH. Install Rust 1.74 or later (https://rustup.rs)")
	}
	index := "sparse+" + j.c.id.base() + "/cargo/" + j.p.Repo + "/index/"
	argv := []string{cargo, "publish", "--registry", "telesphorepo",
		"--config", `registries.telesphorepo.index="` + index + `"`,
		"--config", `registries.telesphorepo.credential-provider="cargo:token"`}
	return j.runTool(argv, j.env("CARGO_REGISTRIES_TELESPHOREPO_TOKEN="+j.s.Token))
}

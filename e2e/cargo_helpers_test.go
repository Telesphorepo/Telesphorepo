//go:build e2e || import || clients

package e2e

import (
	"fmt"
	"net/http"
	"strings"
)

// The real Cargo of the official Rust image, for the Cargo tests of the core, import and clients
// zones.
var rustImage = floatingImage("docker.io/library/rust:latest")

// cargoEnv is the env of a cargo container: plain output and retries of a network failure.
func cargoEnv(extra map[string]string) map[string]string {
	env := map[string]string{"CARGO_TERM_COLOR": "never", "CARGO_TERM_PROGRESS_WHEN": "never", "CARGO_NET_RETRY": "5"}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// cargoRegistry is a .cargo/config.toml under dir that names the registry reg at index (a sparse
// index URL without "sparse+"). replace makes reg the source of every crates.io crate.
func cargoRegistry(dir, reg, index string, replace bool) ContainerFile {
	c := fmt.Sprintf("[registries.%s]\nindex = \"sparse+%s\"\n", reg, index)
	if replace {
		c += fmt.Sprintf("\n[source.crates-io]\nreplace-with = %q\n", reg)
	}
	return ContainerFile{Path: dir + "/.cargo/config.toml", Content: c}
}

// cargoTokenEnv is the env variable of the token of registry reg.
func cargoTokenEnv(reg string) string {
	return "CARGO_REGISTRIES_" + strings.ToUpper(strings.ReplaceAll(reg, "-", "_")) + "_TOKEN"
}

// cargoLib is a library crate in dir: Cargo.toml with the package lines and extra (dependency and
// feature tables), and src/lib.rs whose hello() returns message.
func cargoLib(dir, name, version, extra, message string) []ContainerFile {
	return []ContainerFile{
		{Path: dir + "/Cargo.toml", Content: fmt.Sprintf("[package]\nname = %q\nversion = %q\nedition = \"2021\"\n"+
			"description = \"an e2e crate\"\nlicense = \"MIT\"\n\n%s", name, version, extra)},
		{Path: dir + "/src/lib.rs", Content: fmt.Sprintf("pub fn hello() -> &'static str {\n    %q\n}\n", message)},
	}
}

// cargoBin is a binary crate in dir that depends on deps (Cargo.toml lines of [dependencies]) and
// prints the hello() of each crate of uses.
func cargoBin(dir, deps string, uses ...string) []ContainerFile {
	var main strings.Builder
	main.WriteString("fn main() {\n")
	for _, u := range uses {
		fmt.Fprintf(&main, "    println!(\"{}\", %s::hello());\n", strings.ReplaceAll(u, "-", "_"))
	}
	main.WriteString("}\n")
	return []ContainerFile{
		{Path: dir + "/Cargo.toml", Content: "[package]\nname = \"consumer\"\nversion = \"0.1.0\"\nedition = \"2021\"\npublish = false\n\n[dependencies]\n" + deps},
		{Path: dir + "/src/main.rs", Content: main.String()},
	}
}

// rawToken is a token in the Authorization header without a scheme, as Cargo sends it.
type rawToken string

func (t rawToken) apply(r *http.Request) { r.Header.Set("Authorization", string(t)) }

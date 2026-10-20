//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// The NuGet format with the real .NET SDK: dotnet pack, dotnet nuget push and delete, dotnet add
// package and dotnet run, against hosted, proxy (nuget.org) and group repos over plain HTTP.

var dotnetImage = floatingImage("mcr.microsoft.com/dotnet/sdk:10.0")

// dotnetEnv keeps the SDK quiet and its package cache inside the container, so each restore
// asks the server.
var dotnetEnv = map[string]string{"DOTNET_CLI_TELEMETRY_OPTOUT": "1", "DOTNET_NOLOGO": "1",
	"DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1", "NUGET_PACKAGES": "/tmp/nuget-packages"}

// nugetSource is a package source of a nuget.config: its key, the service index and the read
// credentials ("" user: none).
type nugetSource struct{ key, url, user, pass string }

// nugetConfigFile is /work/nuget.config with only sources (nuget.org cleared), each on plain
// HTTP with allowInsecureConnections.
func nugetConfigFile(sources ...nugetSource) ContainerFile {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<configuration>\n  <packageSources>\n    <clear />\n")
	for _, s := range sources {
		fmt.Fprintf(&b, "    <add key=%q value=%q allowInsecureConnections=\"true\" />\n", s.key, s.url)
	}
	b.WriteString("  </packageSources>\n  <packageSourceCredentials>\n")
	for _, s := range sources {
		if s.user != "" {
			fmt.Fprintf(&b, "    <%s>\n      <add key=\"Username\" value=%q />\n      <add key=\"ClearTextPassword\" value=%q />\n    </%s>\n", s.key, s.user, s.pass, s.key)
		}
	}
	b.WriteString("  </packageSourceCredentials>\n</configuration>\n")
	return ContainerFile{Path: "/work/nuget.config", Content: b.String()}
}

// nugetLib is a class library at dir that packs as package id at version; E2E.Hello.Message()
// returns marker.
func nugetLib(dir, id, version, marker string) []ContainerFile {
	return []ContainerFile{
		{Path: dir + "/Lib.csproj", Content: fmt.Sprintf(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
    <PackageId>%s</PackageId>
    <Version>%s</Version>
    <Authors>e2e</Authors>
    <Description>A package of the e2e tests.</Description>
  </PropertyGroup>
</Project>
`, id, version)},
		{Path: dir + "/Hello.cs", Content: fmt.Sprintf("namespace E2E;\n\npublic static class Hello\n{\n    public static string Message() => %q;\n}\n", marker)},
	}
}

// nugetApp is a console app at /work/app with the code of Program.cs and no package reference:
// the script adds them with dotnet add package.
func nugetApp(program string) []ContainerFile {
	return []ContainerFile{
		{Path: "/work/app/App.csproj", Content: `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net10.0</TargetFramework>
    <ImplicitUsings>enable</ImplicitUsings>
    <NuGetAudit>true</NuGetAudit>
  </PropertyGroup>
</Project>
`},
		{Path: "/work/app/Program.cs", Content: program},
	}
}

// runDotnet runs script in the .NET SDK image with files and the env pairs of env (name, value),
// and fails the test on a non-zero exit.
func runDotnet(t *testing.T, what, script string, files []ContainerFile, env ...string) *ClientResult {
	t.Helper()
	all := map[string]string{}
	for k, v := range dotnetEnv {
		all[k] = v
	}
	for i := 0; i+1 < len(env); i += 2 {
		all[env[i]] = env[i+1]
	}
	r := RunClientResult(t, dotnetImage, ClientOpts{Cmd: []string{script}, Files: files, Env: all, Timeout: 20 * time.Minute})
	if r.ExitCode != 0 {
		t.Fatalf("%s: exit %d\n%s", what, r.ExitCode, r.Output)
	}
	return r
}

// wantOut fails unless out holds each of subs.
func wantOut(t *testing.T, what, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Fatalf("%s: no %q in:\n%s", what, s, out)
		}
	}
}

// nugetVersions reads the flat-container version list of package lid in repo.
func nugetVersions(t *testing.T, s *Server, repo, lid string, auth Auth) (int, []string) {
	t.Helper()
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/nuget/"+repo+"/v3-flatcontainer/"+lid+"/index.json", nil), auth)
	var doc struct {
		Versions []string `json:"versions"`
	}
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("version list of %s in %s: %v: %s", lid, repo, err, b)
		}
	}
	return resp.StatusCode, doc.Versions
}

// nugetLeaves reads the versions of the registration index of lid in repo, with whether each is
// listed. A paged index is not followed: the tests publish few versions.
func nugetLeaves(t *testing.T, s *Server, repo, lid string, auth Auth) map[string]bool {
	t.Helper()
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/nuget/"+repo+"/registration/"+lid+"/index.json", nil), auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("registration of %s in %s: status %d: %s", lid, repo, resp.StatusCode, b)
	}
	if strings.Contains(string(b), "api.nuget.org") {
		t.Fatalf("registration of %s in %s names api.nuget.org: %s", lid, repo, b)
	}
	var doc struct {
		Items []struct {
			Items []struct {
				CatalogEntry struct {
					Version string `json:"version"`
					Listed  *bool  `json:"listed"`
				} `json:"catalogEntry"`
				PackageContent string `json:"packageContent"`
			} `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("registration of %s in %s: %v: %s", lid, repo, err, b)
	}
	out := map[string]bool{}
	for _, pg := range doc.Items {
		for _, it := range pg.Items {
			if !strings.HasPrefix(it.PackageContent, s.InternalURL+"/nuget/"+repo+"/v3-flatcontainer/") {
				t.Fatalf("registration of %s in %s: packageContent %s is not of this repo", lid, repo, it.PackageContent)
			}
			out[it.CatalogEntry.Version] = it.CatalogEntry.Listed == nil || *it.CatalogEntry.Listed
		}
	}
	return out
}

// TestNuGet: dotnet pack and dotnet nuget push to a private hosted repo (a second push of a
// version fails with 409, --skip-duplicate skips it), the package bytes stay unchanged, unlist with
// dotnet nuget delete and relist with POST, search with dotnet package search, and a console app
// that adds the package with dotnet add package (pinned and newest) and runs it, with Basic
// credentials over plain HTTP.
func TestNuGet(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("nuget")
		s.CreateRepo(t, repo, "nuget", false, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, writerPass)
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		readToken := s.CreateToken(t, writer, writerPass, "nuget-e2e")
		read := Basic{writer, readToken}
		id := "E2E.Lib" + randHex(4)
		lid := strings.ToLower(id)
		src := nugetSource{key: "tr", url: s.InternalURL + "/nuget/" + repo + "/index.json", user: writer, pass: readToken}
		apiKey := PublishToken(t, s, writer, repo)

		files := append(nugetLib("/work/lib1", id, "1.0.0", "hello v1"), nugetLib("/work/lib2", id, "1.1.0", "hello v1.1")...)
		files = append(files, nugetConfigFile(src))
		pushed := runDotnet(t, "pack and push", fmt.Sprintf(`set -e
cd /work
dotnet pack lib1 -c Release -o /work/out
dotnet pack lib2 -c Release -o /work/out
dotnet nuget push out/%[1]s.1.0.0.nupkg --source tr --api-key "$API_KEY"
dotnet nuget push out/%[1]s.1.1.0.nupkg --source tr --api-key "$API_KEY"
if dotnet nuget push out/%[1]s.1.0.0.nupkg --source tr --api-key "$API_KEY"; then echo DUPLICATE-ACCEPTED; exit 1; fi
echo DUPLICATE-REFUSED
dotnet nuget push out/%[1]s.1.0.0.nupkg --source tr --api-key "$API_KEY" --skip-duplicate
dotnet nuget delete %[1]s 1.1.0 --source tr --api-key "$API_KEY" --non-interactive
`, id), files, "API_KEY", apiKey)
		wantOut(t, "pack and push", pushed.Output, "DUPLICATE-REFUSED", "409")

		// The stored package has the bytes that dotnet pack wrote.
		nupkg := pushed.ReadFile(t, "/work/out/"+id+".1.0.0.nupkg")
		sum := sha256.Sum256(nupkg)
		head, _ := s.Do(t, s.NewRequest(t, http.MethodHead, "/nuget/"+repo+"/v3-flatcontainer/"+lid+"/1.0.0/"+lid+".1.0.0.nupkg", nil), read)
		if head.StatusCode != http.StatusOK || head.Header.Get("X-Checksum-SHA256") != hex.EncodeToString(sum[:]) {
			t.Fatalf("HEAD of the pushed package: status %d, sha256 %q, want %s", head.StatusCode, head.Header.Get("X-Checksum-SHA256"), hex.EncodeToString(sum[:]))
		}
		if resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/nuget/"+repo+"/index.json", nil), nil); resp.StatusCode != http.StatusUnauthorized ||
			!strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
			t.Fatalf("anonymous service index of a private repo: status %d, WWW-Authenticate %q, want 401 Basic", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}

		// Unlisted: still in the version list, not listed in the registration.
		if st, vs := nugetVersions(t, s, repo, lid, read); st != http.StatusOK || !slices.Equal(vs, []string{"1.0.0", "1.1.0"}) {
			t.Fatalf("version list: status %d, %v, want [1.0.0 1.1.0]", st, vs)
		}
		if l := nugetLeaves(t, s, repo, lid, read); !l["1.0.0"] || l["1.1.0"] {
			t.Fatalf("registration after the unlist of 1.1.0: %v", l)
		}
		req := s.NewRequest(t, http.MethodPost, "/nuget/"+repo+"/package/"+id+"/1.1.0", nil)
		req.Header.Set("X-NuGet-ApiKey", apiKey)
		if resp, b := s.Do(t, req, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("relist 1.1.0: status %d: %s", resp.StatusCode, b)
		}
		if l := nugetLeaves(t, s, repo, lid, read); !l["1.0.0"] || !l["1.1.0"] {
			t.Fatalf("registration after the relist of 1.1.0: %v", l)
		}
		// A password or a read token never pushes.
		req = s.NewRequest(t, http.MethodDelete, "/nuget/"+repo+"/package/"+id+"/1.1.0", nil)
		req.Header.Set("X-NuGet-ApiKey", readToken)
		if resp, _ := s.Do(t, req, nil); resp.StatusCode/100 == 2 {
			t.Fatalf("unlist with a read token: status %d, want a refusal", resp.StatusCode)
		}

		out := runDotnet(t, "add the package and run", fmt.Sprintf(`set -e
cd /work/app
dotnet package search %[2]s --source tr --prerelease
dotnet add package %[1]s --version 1.0.0
dotnet run
dotnet add package %[1]s
dotnet run
`, id, lid[4:]), append(nugetApp("Console.WriteLine(\"MESSAGE \" + E2E.Hello.Message());\n"), nugetConfigFile(src))).Output
		wantOut(t, "add the package and run", out, id, "MESSAGE hello v1\n", "MESSAGE hello v1.1")
		for _, warn := range []string{"NU1900", "NU1905"} {
			if strings.Contains(out, warn) {
				t.Fatalf("restore warned %s:\n%s", warn, out)
			}
		}
	})
}

// TestNuGetProxyGroup: a proxy of nuget.org restores Newtonsoft.Json (registration pages and the
// flat container), and a private group of a hosted repo and the proxy restores both: a package of
// the hosted repo and a package of nuget.org. A package id of the hosted member hides the
// nuget.org package of the same id with higher versions.
func TestNuGetProxyGroup(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		proxy, hosted, group := RandName("nugetorg"), RandName("nuget"), RandName("nugetall")
		s.CreateProxy(t, proxy, "nuget", true, map[string]any{"remote_url": "https://api.nuget.org/v3/index.json"}, false)
		s.CreateRepo(t, hosted, "nuget", false, nil)
		s.CreateGroup(t, group, "nuget", false, []string{hosted, proxy})
		dev, devPass := RandName("dev"), randHex(12)
		s.CreateUser(t, dev, devPass)
		s.SetPerms(t, hosted, dev, "repo.download", "repo.publish")
		s.SetPerms(t, group, dev, "repo.download")
		token := s.CreateToken(t, dev, devPass, "nuget-e2e")

		t.Run("proxy", func(t *testing.T) {
			px := nugetSource{key: "px", url: s.InternalURL + "/nuget/" + proxy + "/index.json"}
			out := runDotnet(t, "restore Newtonsoft.Json through the proxy", `set -e
cd /work/app
dotnet add package Newtonsoft.Json --version 13.0.3
dotnet run
dotnet add package Newtonsoft.Json
dotnet run
`, append(nugetApp("Console.WriteLine(\"JSON \" + Newtonsoft.Json.JsonConvert.SerializeObject(new { a = 1 }));\n"), nugetConfigFile(px))).Output
			wantOut(t, "restore Newtonsoft.Json through the proxy", out, `JSON {"a":1}`)
			if !slices.Contains(assetPathsOf(t, s, proxy, "v3-flatcontainer/newtonsoft.json/13.0.3/"), "v3-flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg") {
				t.Fatalf("the proxy keeps no newtonsoft.json 13.0.3")
			}
			nugetLeaves(t, s, proxy, "newtonsoft.json", nil)
		})

		t.Run("group", func(t *testing.T) {
			id := "E2E.Private" + randHex(4)
			files := append(nugetLib("/work/lib", id, "1.0.0", "private hello"), nugetLib("/work/shadow", "Newtonsoft.Json", "1.0.0", "shadow")...)
			files = append(files, nugetConfigFile(nugetSource{key: "tr", url: s.InternalURL + "/nuget/" + hosted + "/index.json", user: dev, pass: token}))
			runDotnet(t, "push to the hosted member", fmt.Sprintf(`set -e
cd /work
dotnet pack lib -c Release -o /work/out
dotnet pack shadow -c Release -o /work/out
dotnet nuget push out/%s.1.0.0.nupkg --source tr --api-key "$API_KEY"
dotnet nuget push out/Newtonsoft.Json.1.0.0.nupkg --source tr --api-key "$API_KEY"
`, id), files, "API_KEY", PublishToken(t, s, dev, hosted))

			auth := Basic{dev, token}
			if st, vs := nugetVersions(t, s, group, "newtonsoft.json", auth); st != http.StatusOK || !slices.Equal(vs, []string{"1.0.0"}) {
				t.Fatalf("newtonsoft.json in the group: status %d, %v, want only the hosted 1.0.0", st, vs)
			}
			if st, vs := nugetVersions(t, s, proxy, "newtonsoft.json", nil); st != http.StatusOK || !slices.Contains(vs, "13.0.3") {
				t.Fatalf("newtonsoft.json in the proxy: status %d, %v", st, vs)
			}
			if l := nugetLeaves(t, s, group, strings.ToLower(id), auth); !l["1.0.0"] {
				t.Fatalf("registration of %s in the group: %v", id, l)
			}
			if st, _ := nugetVersions(t, s, group, "humanizer.core", nil); st != http.StatusUnauthorized {
				t.Fatalf("anonymous read of the private group: status %d, want 401", st)
			}

			grp := nugetSource{key: "grp", url: s.InternalURL + "/nuget/" + group + "/index.json", user: dev, pass: token}
			out := runDotnet(t, "restore through the group", fmt.Sprintf(`set -e
cd /work/app
dotnet add package %s --version 1.0.0
dotnet add package Humanizer.Core --version 2.14.1
dotnet run
`, id), append(nugetApp("using Humanizer;\nConsole.WriteLine(\"GROUP \" + E2E.Hello.Message() + \" \" + \"some_text\".Humanize());\n"), nugetConfigFile(grp))).Output
			wantOut(t, "restore through the group", out, "GROUP private hello Some text")
		})
	})
}

// assetPathsOf lists the asset paths of repo under prefix.
func assetPathsOf(t *testing.T, s *Server, repo, prefix string) []string {
	t.Helper()
	return mavenAssetPaths(t, s, repo, prefix, s.Admin())
}

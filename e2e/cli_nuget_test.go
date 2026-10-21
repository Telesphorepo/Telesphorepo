//go:build clients

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

var cliDotnetImage = floatingImage("mcr.microsoft.com/dotnet/sdk:10.0")

// cliDotnetEnv keeps the .NET SDK quiet in the containers of the CLI tests.
var cliDotnetEnv = map[string]string{"DOTNET_CLI_TELEMETRY_OPTOUT": "1", "DOTNET_NOLOGO": "1", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1"}

// cliNuGetLib is a class library at /work/lib that packs as package id at version.
func cliNuGetLib(id, version, marker string) []ContainerFile {
	return []ContainerFile{
		{Path: "/work/lib/Lib.csproj", Content: fmt.Sprintf(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
    <PackageId>%s</PackageId>
    <Version>%s</Version>
    <Authors>e2e</Authors>
    <Description>A package of the CLI tests.</Description>
  </PropertyGroup>
</Project>
`, id, version)},
		{Path: "/work/lib/Hello.cs", Content: fmt.Sprintf("namespace E2E;\n\npublic static class Hello\n{\n    public static string Message() => %q;\n}\n", marker)},
	}
}

func cliDotnetOpts(files []ContainerFile, devices string, env map[string]string) ClientOpts {
	all := map[string]string{}
	for k, v := range cliDotnetEnv {
		all[k] = v
	}
	for k, v := range env {
		all[k] = v
	}
	return ClientOpts{Files: files, Env: all, Mounts: map[string]string{devices: "/root/.telesphorepo"}, Timeout: 20 * time.Minute}
}

// TestCLINuGet: setup and publish run dotnet pack and push the package with the session as the API
// key; publish FILE pushes a package that dotnet pack wrote before; a console app restores both
// versions from the repo.
func TestCLINuGet(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("nuget")
		s.CreateRepo(t, repo, "nuget", false, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		devices := NewDataVolume(t)
		id := "E2E.Cli" + randHex(4)
		lid := strings.ToLower(id)

		out := c.mustRun(t, cliDotnetImage, "telesphorepo publish of a .NET project", `set -e
cd /work/lib
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`, cliDotnetOpts(cliNuGetLib(id, "1.0.0", "cli hello 1"), devices, map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}))
		wantText(t, "telesphorepo publish of a .NET project", out, "Publishing to "+repo+" (nuget)", "pack -c Release -o ", "Pushed "+id+".1.0.0.nupkg")
		requireAssets(t, s, repo, "v3-flatcontainer/"+lid+"/",
			"v3-flatcontainer/"+lid+"/1.0.0/"+lid+".1.0.0.nupkg", "v3-flatcontainer/"+lid+"/1.0.0/"+lid+".nuspec")

		out = c.mustRun(t, cliDotnetImage, "telesphorepo publish FILE", fmt.Sprintf(`set -e
cd /work/lib
dotnet pack -c Release -o /tmp/pkgs
telesphorepo publish /tmp/pkgs/%s.2.0.0.nupkg
`, id), cliDotnetOpts(append(cliNuGetLib(id, "2.0.0", "cli hello 2"), projectFile(s, "/work/lib", repo)), devices, nil))
		wantText(t, "telesphorepo publish FILE", out, "Pushed "+id+".2.0.0.nupkg")
		wantNoText(t, "telesphorepo publish FILE", out, "pack -c Release -o /tmp/telesphorepo")

		// Two packages at once: the large one goes in chunks and the push finishes it, the small one
		// goes in one push. The lines say what was pushed in the order of the arguments.
		big := []ContainerFile{{Path: "/work/big/Big.csproj", Content: fmt.Sprintf(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
    <PackageId>%s.Big</PackageId>
    <Version>3.0.0</Version>
    <Authors>e2e</Authors>
    <Description>A large package of the CLI tests.</Description>
  </PropertyGroup>
  <ItemGroup>
    <None Include="big.bin" Pack="true" PackagePath="content" />
  </ItemGroup>
</Project>
`, id)}}
		what := "telesphorepo publish of a large and a small package"
		out = c.mustRun(t, cliDotnetImage, what, fmt.Sprintf(`set -e
head -c 80000000 /dev/urandom > /work/big/big.bin
dotnet pack /work/big -c Release -o /tmp/pkgs
dotnet pack /work/lib -c Release -o /tmp/pkgs
cd /work/lib
telesphorepo publish /tmp/pkgs/%[1]s.Big.3.0.0.nupkg /tmp/pkgs/%[1]s.3.0.0.nupkg
`, id), cliDotnetOpts(append(append(cliNuGetLib(id, "3.0.0", "cli hello 3"), big...), projectFile(s, "/work/lib", repo)), devices, nil))
		bigLine, smallLine := "Pushed "+id+".Big.3.0.0.nupkg", "Pushed "+id+".3.0.0.nupkg"
		wantText(t, what, out, "Uploading /tmp/pkgs/"+id+".Big.3.0.0.nupkg", "in chunks of", bigLine, smallLine)
		if strings.Index(out, bigLine) > strings.Index(out, smallLine) {
			t.Fatalf("%s: the lines are not in the order of the arguments:\n%s", what, out)
		}
		requireAssets(t, s, repo, "v3-flatcontainer/"+lid+".big/", "v3-flatcontainer/"+lid+".big/3.0.0/"+lid+".big.3.0.0.nupkg",
			"v3-flatcontainer/"+lid+".big/3.0.0/"+lid+".big.nuspec")

		token := s.CreateToken(t, dev, "dev-pass-1234", "cli-nuget")
		config := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="tr" value="%s/nuget/%s/index.json" allowInsecureConnections="true" />
  </packageSources>
  <packageSourceCredentials>
    <tr>
      <add key="Username" value="%s" />
      <add key="ClearTextPassword" value="%s" />
    </tr>
  </packageSourceCredentials>
</configuration>
`, s.InternalURL, repo, dev, token)
		code, out := RunClient(t, cliDotnetImage, ClientOpts{Cmd: []string{fmt.Sprintf(`set -e
cd /work/app
dotnet add package %[1]s --version 1.0.0
dotnet run
dotnet add package %[1]s --version 2.0.0
dotnet run
`, id)}, Env: cliDotnetEnv, Timeout: 20 * time.Minute, Files: []ContainerFile{
			{Path: "/work/nuget.config", Content: config},
			{Path: "/work/app/App.csproj", Content: "<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup>\n    <OutputType>Exe</OutputType>\n    <TargetFramework>net10.0</TargetFramework>\n  </PropertyGroup>\n</Project>\n"},
			{Path: "/work/app/Program.cs", Content: "System.Console.WriteLine(\"MESSAGE \" + E2E.Hello.Message());\n"},
		}})
		if code != 0 {
			t.Fatalf("restore the published packages: exit %d\n%s", code, out)
		}
		wantText(t, "restore the published packages", out, "MESSAGE cli hello 1", "MESSAGE cli hello 2")

		var sessions []publishLogItem
		for _, it := range publishLog(t, s, repo) {
			if it.Via == "device" {
				sessions = append(sessions, it)
			}
		}
		if len(sessions) < 2 {
			t.Fatalf("publish log of %s: %d device sessions, want 2", repo, len(sessions))
		}
		if resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/nuget/"+repo+"/v3-flatcontainer/"+lid+"/index.json", nil), Basic{dev, token}); resp.StatusCode != http.StatusOK {
			t.Fatalf("version list after the CLI publish: status %d", resp.StatusCode)
		}
	})
}

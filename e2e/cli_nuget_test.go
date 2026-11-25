//go:build clients

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var cliDotnetImage = floatingImage("mcr.microsoft.com/dotnet/sdk:10.0")

// cliDotnetEnv keeps the .NET SDK quiet in the containers of the CLI tests.
var cliDotnetEnv = map[string]string{"DOTNET_CLI_TELEMETRY_OPTOUT": "1", "DOTNET_NOLOGO": "1", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1"}

// cliNuGetLib is a class library at /work/lib that packs as package id at version.
func cliNuGetLib(id, version string) []ContainerFile {
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
		{Path: "/work/lib/Hello.cs", Content: "namespace E2E;\n\npublic static class Hello\n{\n    public static string Message() => \"hello\";\n}\n"},
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
// key; publish FILE pushes packages that dotnet pack wrote before, without a pack of its own.
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
`, cliDotnetOpts(cliNuGetLib(id, "1.0.0"), devices, map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}))
		wantText(t, "telesphorepo publish of a .NET project", out, "dotnet pack", "Pushed "+id+".1.0.0.nupkg")
		requireAssets(t, s, repo, "v3-flatcontainer/"+lid+"/",
			"v3-flatcontainer/"+lid+"/1.0.0/"+lid+".1.0.0.nupkg", "v3-flatcontainer/"+lid+"/1.0.0/"+lid+".nuspec")

		// Two packages at once: the large one (random bytes, so the package is no smaller) goes in
		// chunks and the push finishes it, the small one goes in one push. The lines say what was
		// pushed in the order of the arguments.
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
head -c %[2]d /dev/urandom > /work/big/big.bin
dotnet pack /work/big -c Release -o /tmp/pkgs
dotnet pack /work/lib -c Release -o /tmp/pkgs
cd /work/lib
telesphorepo publish /tmp/pkgs/%[1]s.Big.3.0.0.nupkg /tmp/pkgs/%[1]s.3.0.0.nupkg
`, id, cliBigUpload(t)), cliDotnetOpts(append(append(cliNuGetLib(id, "3.0.0"), big...), projectFile(s, "/work/lib", repo)), devices, nil))
		bigLine, smallLine := "Pushed "+id+".Big.3.0.0.nupkg", "Pushed "+id+".3.0.0.nupkg"
		wantText(t, what, out, "Uploading /tmp/pkgs/"+id+".Big.3.0.0.nupkg", "in chunks of", bigLine, smallLine)
		if strings.Index(out, bigLine) > strings.Index(out, smallLine) {
			t.Fatalf("%s: the lines are not in the order of the arguments:\n%s", what, out)
		}
		wantNoText(t, what, out, "dotnet pack")
		requireAssets(t, s, repo, "v3-flatcontainer/"+lid+".big/", "v3-flatcontainer/"+lid+".big/3.0.0/"+lid+".big.3.0.0.nupkg",
			"v3-flatcontainer/"+lid+".big/3.0.0/"+lid+".big.nuspec")
	})
}

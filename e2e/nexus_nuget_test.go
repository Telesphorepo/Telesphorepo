//go:build import

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The Nexus import of nuget repos (part of TestNexus): a package that dotnet pack wrote, uploaded
// to a Nexus nuget hosted repo, imported into a new hosted repo of s, then restored and run from
// s with the real .NET SDK.

// nexusDotnetVersion is the .NET version of the SDK image and the target framework of the
// projects.
const nexusDotnetVersion = "10.0"

var nexusDotnetImage = floatingImage("mcr.microsoft.com/dotnet/sdk:" + nexusDotnetVersion)

// nexusDotnetTFM is the target framework moniker of nexusDotnetVersion.
const nexusDotnetTFM = "net" + nexusDotnetVersion

var nexusDotnetEnv = map[string]string{"DOTNET_CLI_TELEMETRY_OPTOUT": "1", "DOTNET_NOLOGO": "1", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1"}

func nexusNuGet(t *testing.T, n *Nexus, s *Server) {
	src := RandName("src-nuget")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/nuget/hosted", map[string]any{
		"name": src, "online": true, "storage": nexusStorage,
	}, nil)

	id := "E2E.Nexus" + randHex(4)
	lid := strings.ToLower(id)
	packed := RunClientResult(t, nexusDotnetImage, ClientOpts{Cmd: []string{"set -e\ncd /work/lib\ndotnet pack -c Release -o /work/out"},
		Env: nexusDotnetEnv, Timeout: 20 * time.Minute, Files: []ContainerFile{
			{Path: "/work/lib/Lib.csproj", Content: fmt.Sprintf("<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup>\n    <TargetFramework>%s</TargetFramework>\n"+
				"    <PackageId>%s</PackageId>\n    <Version>1.2.3</Version>\n    <Authors>e2e</Authors>\n    <Description>A package of the import test.</Description>\n"+
				"  </PropertyGroup>\n</Project>\n", nexusDotnetTFM, id)},
			{Path: "/work/lib/Hello.cs", Content: "namespace E2E;\n\npublic static class Hello\n{\n    public static string Message() => \"from nexus\";\n}\n"},
		}})
	if packed.ExitCode != 0 {
		t.Fatalf("dotnet pack: exit %d\n%s", packed.ExitCode, packed.Output)
	}
	nupkg := packed.ReadFile(t, "/work/out/"+id+".1.2.3.nupkg")
	n.upload(t, src, nexusPart{field: "nuget.asset", value: id + ".1.2.3.nupkg", data: nupkg})
	paths := n.assetPaths(t, src)
	for deadline := time.Now().Add(time.Minute); len(paths) == 0; paths = n.assetPaths(t, src) {
		if time.Now().After(deadline) {
			t.Fatalf("the Nexus nuget repo %s lists no asset after a minute", src)
		}
		time.Sleep(time.Second)
	}
	t.Logf("assets of the Nexus nuget repo: %v", paths)

	source := nexusCreateSource(t, s, n)
	if r := nexusGetSource(t, s, source.ID).repo(t, src); r.SourceFormat != "nuget" || r.Type != "hosted" || !r.Supported {
		t.Fatalf("the Nexus nuget repo: %+v, want a supported nuget hosted repo", r)
	}
	target := RandName("nxnu")
	first := nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": true}})
	if r := first.repo(t, src); r.Imported != 1 || r.Failed != 0 {
		t.Fatalf("first sync of the nuget repo: %+v, want one package imported", r)
	}
	if info := nexusRepoInfoOf(t, s, target); info.Format != "nuget" || info.Kind != "hosted" || !info.AnonymousRead {
		t.Fatalf("the nuget target: %+v", info)
	}
	resp, _ := s.Do(t, s.NewRequest(t, http.MethodHead, "/nuget/"+target+"/v3-flatcontainer/"+lid+"/1.2.3/"+lid+".1.2.3.nupkg", nil), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD of the imported package: status %d", resp.StatusCode)
	}

	code, out := RunClient(t, nexusDotnetImage, ClientOpts{Cmd: []string{fmt.Sprintf(`set -e
cd /work/app
dotnet add package %s --version 1.2.3
dotnet run
`, id)}, Env: nexusDotnetEnv, Timeout: 20 * time.Minute, Files: []ContainerFile{
		{Path: "/work/nuget.config", Content: fmt.Sprintf("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<configuration>\n  <packageSources>\n    <clear />\n"+
			"    <add key=\"tr\" value=\"%s/nuget/%s/index.json\" allowInsecureConnections=\"true\" />\n  </packageSources>\n</configuration>\n", s.InternalURL, target)},
		{Path: "/work/app/App.csproj", Content: "<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup>\n    <OutputType>Exe</OutputType>\n    <TargetFramework>" + nexusDotnetTFM + "</TargetFramework>\n  </PropertyGroup>\n</Project>\n"},
		{Path: "/work/app/Program.cs", Content: "System.Console.WriteLine(\"MESSAGE \" + E2E.Hello.Message());\n"},
	}})
	if code != 0 || !strings.Contains(out, "MESSAGE from nexus") {
		t.Fatalf("restore the imported package: exit %d:\n%s", code, out)
	}

	again := nexusSyncNow(t, s, source.ID)
	if r := again.repo(t, src); r.Imported != 0 || r.Failed != 0 {
		t.Fatalf("second sync of the nuget repo: %+v, want nothing new", r)
	}
}

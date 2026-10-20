package main

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// nugetAPIKey is the header of a NuGet push: the publish session goes in it.
const nugetAPIKey = "X-NuGet-ApiKey"

// runDotnet publishes a .NET project: dotnet pack -c Release into the temp folder, then a push of
// each package that it wrote. With FILE arguments it pushes those packages and builds nothing.
// dotnet pack gets no token: the CLI pushes itself.
func runDotnet(j *job) error {
	if len(j.files) > 0 {
		if err := openFiles(j.files); err != nil {
			return err
		}
		return pushNupkgs(j, j.files)
	}
	dotnet, err := exec.LookPath("dotnet")
	if err != nil {
		return errors.New("dotnet is not on the PATH. Install the .NET SDK")
	}
	out := filepath.Join(j.tmp, "nupkg")
	if err := os.Mkdir(out, 0o700); err != nil {
		return err
	}
	if err := j.runTool([]string{dotnet, "pack", "-c", "Release", "-o", out}, childEnv()); err != nil {
		return err
	}
	all, _ := filepath.Glob(filepath.Join(out, "*.nupkg"))
	var pkgs []string
	for _, f := range all {
		if !strings.HasSuffix(strings.ToLower(f), ".symbols.nupkg") {
			pkgs = append(pkgs, f)
		}
	}
	if len(pkgs) == 0 {
		return errors.New("dotnet pack wrote no package. A project packs when it is packable: <IsPackable>true</IsPackable> in the project file (a test project is not)")
	}
	return pushNupkgs(j, pkgs)
}

// pushNupkgs pushes each package to the NuGet repo of the job: PUT /nuget/<repo>/package with the
// package as the first part of a multipart body and the session in X-NuGet-ApiKey. The bytes go
// unchanged, so a signed package keeps its signature.
func pushNupkgs(j *job, files []string) error {
	p := "/nuget/" + url.PathEscape(j.p.Repo) + "/package"
	for _, f := range files {
		if !strings.HasSuffix(strings.ToLower(f), ".nupkg") {
			return fmt.Errorf("%s is not a .nupkg package", f)
		}
		if err := pushNupkg(j, p, f); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Pushed %s to %s\n", filepath.Base(f), j.p.Repo)
	}
	return nil
}

func pushNupkg(j *job, p, f string) error {
	in, err := os.Open(f)
	if err != nil {
		return err
	}
	defer in.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("package", "package.nupkg")
		if err == nil {
			_, err = io.Copy(part, in)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	defer pr.Close()
	fmt.Fprintf(os.Stderr, "PUT %s%s (%s)\n", j.c.id.base(), p, filepath.Base(f))
	_, err = j.c.call(j.ctx, request{method: http.MethodPut, path: p, body: pr, size: -1, ctype: mw.FormDataContentType(),
		header: map[string]string{nugetAPIKey: j.s.Token}, timeout: -1})
	return err
}

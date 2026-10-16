package maven

import (
	_ "embed"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
)

// The parts of the project setup scripts (GET /setup.sh and /setup.ps1 of the publish module)
// for Gradle and Maven projects: each adds the Telesphorepo plugin or extension of the clients
// version to the project's build files. See format.SetupFragment for the functions they define.

//go:embed setup_gradle.sh
var gradleSh string

//go:embed setup_gradle.ps1
var gradlePs1 string

//go:embed setup_maven.sh
var mavenSh string

//go:embed setup_maven.ps1
var mavenPs1 string

func withVersion(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "@@VERSION@@", clientsVersion)
}

func gradleSetup() *format.SetupFragment {
	return &format.SetupFragment{Project: "settings.gradle.kts, settings.gradle", Sh: withVersion(gradleSh), PS1: withVersion(gradlePs1)}
}

func mavenSetup() *format.SetupFragment {
	return &format.SetupFragment{Project: "pom.xml", Sh: withVersion(mavenSh), PS1: withVersion(mavenPs1)}
}

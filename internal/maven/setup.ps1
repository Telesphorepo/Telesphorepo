# Telesphorepo project setup. Run it in the folder of a Gradle or Maven project:
#
#   & ([scriptblock]::Create((irm @@URL@@/setup.ps1))) REPO [-Ci none|github|jenkins-pipeline|jenkins-freestyle] [-Yes]
#
# It sets up the project to publish to the repository REPO on @@URL@@. It changes only files of
# the project, never ~/.m2 or ~/.gradle, and it writes no secret. A second run changes nothing.
param(
    [Parameter(Position = 0)][string]$Repo,
    [string]$Ci,
    [switch]$Yes,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2

$Url = '@@URL@@'
$Version = '@@VERSION@@'
$Credential = '@@CREDENTIAL@@'
$JenkinsNote = '@@JENKINS_NOTE@@'
$PluginId = 'com.telesphorepo.publish'
$ExtensionId = 'telesphorepo-maven-extension'

$GitHubWorkflow = @'
@@GITHUB_WORKFLOW@@
'@

$JenkinsfileText = @'
@@JENKINSFILE@@
'@

function Stop-Setup([string]$Message) { throw "telesphorepo setup: $Message" }

if ($Help) {
    Write-Host @"
Set up this project to publish to $Url.

Examples:
  & ([scriptblock]::Create((irm $Url/setup.ps1))) releases -Ci none
  & ([scriptblock]::Create((irm $Url/setup.ps1))) releases -Ci github
  & ([scriptblock]::Create((irm $Url/setup.ps1))) releases -Ci jenkins-pipeline -Yes

  REPO          the repository to publish to
  -Ci WHERE     where you publish from besides your computer: none, github,
                jenkins-pipeline or jenkins-freestyle
  -Yes          change files of the project without a question

Run it in the folder with settings.gradle.kts, settings.gradle or pom.xml.
"@
    return
}

$Interactive = [Environment]::UserInteractive -and -not [Console]::IsInputRedirected
$Here = (Get-Location).ProviderPath
function Get-ProjectPath([string]$Name) { return [IO.Path]::Combine($Here, $Name.Replace('/', [IO.Path]::DirectorySeparatorChar)) }

# ---------------------------------------------------------------- the build tool

if (Test-Path -LiteralPath (Get-ProjectPath 'settings.gradle.kts') -PathType Leaf) {
    $Tool = 'gradle'; $Settings = 'settings.gradle.kts'; $Kts = $true
} elseif (Test-Path -LiteralPath (Get-ProjectPath 'settings.gradle') -PathType Leaf) {
    $Tool = 'gradle'; $Settings = 'settings.gradle'; $Kts = $false
} elseif (Test-Path -LiteralPath (Get-ProjectPath 'pom.xml') -PathType Leaf) {
    $Tool = 'maven'
} else {
    Stop-Setup 'this folder has no settings.gradle.kts, settings.gradle or pom.xml. Run the command in the folder of your project.'
}

# ---------------------------------------------------------------- the questions

if (-not $Repo) {
    if (-not $Interactive) { Stop-Setup "name the repository: & ([scriptblock]::Create((irm $Url/setup.ps1))) REPO" }
    $Repo = (Read-Host 'Which repository do you publish to?').Trim()
}
if ($Repo -cnotmatch '^[a-z0-9][a-z0-9._-]*$') {
    Stop-Setup "`"$Repo`" is not a repository name. Use the name the server shows, such as releases."
}

if (-not $PSBoundParameters.ContainsKey('Ci') -and $Interactive) {
    Write-Host 'Where will you publish from?'
    Write-Host '  1  Your computer only'
    Write-Host '  2  GitHub Actions'
    Write-Host '  3  Jenkins with a Jenkinsfile'
    Write-Host '  4  Jenkins freestyle job'
    $answer = (Read-Host 'Type 1, 2, 3 or 4 [1]').Trim()
    switch ($answer) {
        '' { $Ci = '' }
        '1' { $Ci = '' }
        '2' { $Ci = 'github' }
        '3' { $Ci = 'jenkins-pipeline' }
        '4' { $Ci = 'jenkins-freestyle' }
        default { Stop-Setup "`"$answer`" is not 1, 2, 3 or 4" }
    }
}
if (-not $Ci -or $Ci -ceq 'none') { $Ci = '' }
if (@('', 'github', 'jenkins-pipeline', 'jenkins-freestyle') -cnotcontains $Ci) {
    Stop-Setup "-Ci takes none, github, jenkins-pipeline or jenkins-freestyle, not `"$Ci`""
}

# ---------------------------------------------------------------- staged changes

# Each change goes to a working copy first: a list of lines, the line end and the encoding of the
# file. Nothing in the project changes until the end, after the person agrees.
$Work = [ordered]@{}
$Plan = New-Object System.Collections.Generic.List[string]
$Manual = New-Object System.Collections.Generic.List[string]
$Notes = New-Object System.Collections.Generic.List[string]

function Get-Work([string]$Name) {
    if (-not $Work.Contains($Name)) {
        $path = Get-ProjectPath $Name
        $copy = @{ Exists = $false; Changed = $false; Lines = (New-Object System.Collections.Generic.List[string]); Eol = "`n"; Bom = $false }
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            $bytes = [IO.File]::ReadAllBytes($path)
            $copy.Exists = $true
            $copy.Bom = $bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF
            $text = [IO.File]::ReadAllText($path)
            if ($text.Contains("`r`n")) { $copy.Eol = "`r`n" }
            $parts = $text -split "`r?`n"
            $n = $parts.Length
            if ($n -gt 0 -and $parts[$n - 1] -eq '') { $n-- }
            for ($i = 0; $i -lt $n; $i++) { $copy.Lines.Add($parts[$i]) }
        }
        $Work[$Name] = $copy
    }
    return $Work[$Name]
}

function Get-Text($copy) { return (($copy.Lines | ForEach-Object { $_ }) -join "`n") }

function Add-Plan([string]$Name, [string]$What, [string]$Text) {
    $Plan.Add("${Name}: $What")
    foreach ($l in ($Text -split "`n")) { $Plan.Add("    + $l") }
}

function Add-Manual([string]$Name, [string]$Why, [string]$Text) {
    $Manual.Add('')
    $Manual.Add("${Name}: $Why")
    $Manual.Add('')
    foreach ($l in ($Text -split "`n")) { $Manual.Add("    $l") }
}

# Add-Lines puts the lines of Text after line At (1-based; 0: before the first line; -1: at the end).
function Add-Lines($copy, [int]$At, [string]$Text) {
    $new = $Text -split "`n"
    if ($At -lt 0) { $At = $copy.Lines.Count }
    $copy.Lines.InsertRange($At, [string[]]$new)
    $copy.Changed = $true
}

# Find-Block returns the first and the last line (1-based) of the top-level block "Name {": 0, 0
# when there is none; a last line of -1 when its braces do not close.
function Find-Block($copy, [string]$Name) {
    $start = 0; $depth = 0
    for ($i = 0; $i -lt $copy.Lines.Count; $i++) {
        $l = $copy.Lines[$i]
        if ($start -eq 0) {
            if ($l -cmatch "^$Name[ \t]*\{") { $start = $i + 1 } else { continue }
        }
        $depth += ([regex]::Matches($l, '\{')).Count - ([regex]::Matches($l, '\}')).Count
        if ($depth -le 0) { return @($start, ($i + 1)) }
    }
    if ($start -gt 0) { return @($start, -1) }
    return @(0, 0)
}

function Test-Line($copy, [string]$Pattern) {
    foreach ($l in $copy.Lines) { if ($l -cmatch $Pattern) { return $true } }
    return $false
}

function Get-GradleCommand {
    if (Test-Path -LiteralPath (Get-ProjectPath 'gradlew') -PathType Leaf) { return './gradlew' }
    return 'gradle'
}

# ---------------------------------------------------------------- Gradle

function Set-Gradle {
    $f = $Settings
    $c = Get-Work $f
    if ($Kts) {
        $idLine = "id(`"$PluginId`") version `"$Version`""
        $block = "telesphorepo {`n    url = `"$Url`"`n    repository = `"$Repo`""
        $oidc = '    oidcTokenEnv = "OIDC_TOKEN"'
    } else {
        $idLine = "id '$PluginId' version '$Version'"
        $block = "telesphorepo {`n    url = '$Url'`n    repository = '$Repo'"
        $oidc = "    oidcTokenEnv = 'OIDC_TOKEN'"
    }
    if ($Ci -like 'jenkins-*') { $block += "`n$oidc" }
    $block += "`n}"
    $plugins = "plugins {`n    $idLine`n}"

    if (-not (Get-Text $c).Contains($PluginId)) {
        $p = Find-Block $c 'plugins'
        $pm = (Find-Block $c 'pluginManagement')[1]
        $bs = (Find-Block $c 'buildscript')[1]
        if ($p[0] -gt 0 -and $p[1] -gt $p[0]) {
            Add-Lines $c $p[0] "    $idLine"
            Add-Plan $f 'add the plugin to the plugins block' "    $idLine"
        } elseif ($p[0] -gt 0 -or $pm -lt 0 -or $bs -lt 0 -or (Test-Line $c '^plugins\s*$')) {
            Add-Manual $f 'add the id line to the plugins block, or this block at the top (only pluginManagement can come before it)' $plugins
        } else {
            $after = [Math]::Max($pm, $bs)
            if ($after -gt 0) {
                Add-Lines $c $after "`n$plugins"
            } else {
                # At the top, after the comments and imports that open the file.
                $after = 0
                while ($after -lt $c.Lines.Count -and $c.Lines[$after] -cmatch '^(\s*$|\s*//|import\s|@file:)') { $after++ }
                if ($after -gt 0) { Add-Lines $c $after "`n$plugins`n" } else { Add-Lines $c 0 "$plugins`n" }
            }
            Add-Plan $f 'add the plugins block at the top' $plugins
        }
    }

    $t = Find-Block $c 'telesphorepo'
    if ($t[0] -gt 0) {
        if (-not (Test-Line $c ("repository\s*=\s*[`"']" + [regex]::Escape($Repo) + "[`"']"))) {
            $Notes.Add("$f has a telesphorepo block for another repository. Check its url and repository.")
        }
        if ($Ci -like 'jenkins-*' -and -not (Get-Text $c).Contains('oidcTokenEnv')) {
            Add-Lines $c $t[0] $oidc
            Add-Plan $f 'add the Jenkins identity token to the telesphorepo block' $oidc
        }
    } else {
        Add-Lines $c -1 "`n$block"
        Add-Plan $f 'add the telesphorepo block at the end' $block
    }
}

# ---------------------------------------------------------------- Maven

function Find-OnlyLine($copy, [string]$Tag) {
    $count = 0; $at = 0
    for ($i = 0; $i -lt $copy.Lines.Count; $i++) {
        $l = $copy.Lines[$i]
        if ($l.Contains($Tag)) { $count++ }
        if ($l -cmatch ('^\s*' + [regex]::Escape($Tag) + '\s*$')) { $at = $i + 1 }
    }
    if ($count -eq 1) { return $at }
    return 0
}

function Set-Maven {
    $f = '.mvn/extensions.xml'
    $ext = "  <extension>`n    <groupId>com.telesphorepo</groupId>`n    <artifactId>$ExtensionId</artifactId>`n    <version>$Version</version>`n  </extension>"
    $c = Get-Work $f
    if (-not $c.Exists) {
        Add-Lines $c 0 "<extensions>`n$ext`n</extensions>"
        Add-Plan $f 'create it' (Get-Text $c)
    } elseif (-not (Get-Text $c).Contains($ExtensionId)) {
        $at = Find-OnlyLine $c '</extensions>'
        if ($at -gt 0) {
            Add-Lines $c ($at - 1) $ext
            Add-Plan $f 'add the Telesphorepo extension' $ext
        } else {
            Add-Manual $f 'add this extension inside the extensions element' $ext
        }
    }

    $f = '.mvn/maven.config'
    $line = "-Dtelesphorepo.url=$Url"
    $c = Get-Work $f
    if (-not $c.Exists) {
        Add-Lines $c 0 $line
        Add-Plan $f 'create it' $line
    } elseif ((Get-Text $c).Contains('-Dtelesphorepo.url=')) {
        if (-not (Get-Text $c).Contains($line)) { $Notes.Add("$f names another Telesphorepo server. Check the line -Dtelesphorepo.url.") }
    } else {
        Add-Lines $c -1 $line
        Add-Plan $f 'add the server address' $line
    }

    $f = 'pom.xml'
    $c = Get-Work $f
    $target = "$Url/maven/$Repo"
    if ((Get-Text $c).Contains('<distributionManagement')) {
        if (-not (Get-Text $c).Contains("$target<")) { $Notes.Add("pom.xml has a distributionManagement block that does not name $target. Check it.") }
    } else {
        $i = '  '
        foreach ($l in $c.Lines) { if ($l -cmatch '^([ \t]*)<modelVersion>') { if ($Matches[1]) { $i = $Matches[1] }; break } }
        $dm = "$i<distributionManagement>`n$i$i<repository>`n$i$i$i<id>$Repo</id>`n$i$i$i<url>$target</url>`n$i$i</repository>" +
            "`n$i$i<snapshotRepository>`n$i$i$i<id>$Repo</id>`n$i$i$i<url>$target</url>`n$i$i</snapshotRepository>`n$i</distributionManagement>"
        $at = Find-OnlyLine $c '</project>'
        if ($at -gt 0) {
            Add-Lines $c ($at - 1) $dm
            Add-Plan $f 'add distributionManagement' $dm
        } else {
            Add-Manual $f 'add this block inside the project element' $dm
        }
    }
}

# ---------------------------------------------------------------- CI

# Set-CiFile writes a new CI file. It keeps a file with other content.
function Set-CiFile([string]$Name, [string]$Text) {
    $c = Get-Work $Name
    if (-not $c.Exists) {
        Add-Lines $c 0 $Text
        Add-Plan $Name 'create it' $Text
    } elseif ((Get-Text $c).TrimEnd("`n") -cne $Text) {
        Add-Manual $Name 'the file exists with other content. Compare it with this one' $Text
    }
}

function Set-Ci {
    if ($Tool -eq 'gradle') {
        $g = Get-GradleCommand
        $build = "$g build"; $publish = "$g publish"; $ghPublish = "$g publish"
    } else {
        $build = 'mvn -B verify'; $publish = 'mvn -B deploy -DskipTests -Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN'; $ghPublish = 'mvn -B deploy'
    }
    switch ($Ci) {
        'github' {
            Set-CiFile '.github/workflows/publish.yml' ($GitHubWorkflow.Replace("`r`n", "`n").TrimEnd("`n").Replace('%PUBLISH%', $ghPublish))
        }
        'jenkins-pipeline' {
            Set-CiFile 'Jenkinsfile' ($JenkinsfileText.Replace("`r`n", "`n").TrimEnd("`n").Replace('%REPO%', $Repo).Replace('%BUILD%', $build).Replace('%PUBLISH%', $publish))
        }
    }
}

# ---------------------------------------------------------------- run

if ($Tool -eq 'gradle') { Set-Gradle } else { Set-Maven }
if ($Ci) { Set-Ci }

# Render each working copy and compare it with its file: a new file, an edit, or no change.
$New = New-Object System.Collections.Generic.List[string]
$Edit = New-Object System.Collections.Generic.List[string]
$Bytes = @{}
foreach ($name in $Work.Keys) {
    $c = $Work[$name]
    if (-not $c.Changed) { continue }
    $text = (($c.Lines | ForEach-Object { $_ }) -join $c.Eol) + $c.Eol
    $enc = New-Object System.Text.UTF8Encoding($c.Bom)
    $body = $enc.GetBytes($text)
    if ($c.Bom) { $body = [byte[]]($enc.GetPreamble() + $body) }
    if ($c.Exists) {
        $Edit.Add($name)
    } else {
        $New.Add($name)
    }
    $Bytes[$name] = $body
}

if ($New.Count + $Edit.Count -gt 0) {
    Write-Host "Changes to the project for $Url/maven/${Repo}:"
    Write-Host ''
    $Plan | ForEach-Object { Write-Host $_ }
    Write-Host ''
    if ($Edit.Count -gt 0 -and -not $Yes) {
        if (-not $Interactive) { Stop-Setup 'run the command again with -Yes to change these files' }
        $answer = (Read-Host 'Change these files? [y/N]').Trim()
        if (@('y', 'yes') -notcontains $answer.ToLowerInvariant()) { Stop-Setup 'no file changed' }
    }
    foreach ($name in @($New) + @($Edit)) {
        $path = Get-ProjectPath $name
        $dir = [IO.Path]::GetDirectoryName($path)
        if (-not (Test-Path -LiteralPath $dir)) { [void](New-Item -ItemType Directory -Path $dir -Force) }
        [IO.File]::WriteAllBytes($path, $Bytes[$name])
    }
}

if ($Manual.Count -gt 0) {
    Write-Host 'Paste these blocks by hand. The file is too unusual to change safely.'
    $Manual | ForEach-Object { Write-Host $_ }
    Write-Host ''
}
if ($Notes.Count -gt 0) {
    $Notes | ForEach-Object { Write-Host $_ }
    Write-Host ''
}

# Get-BuildFile returns the root build file of the Gradle project.
function Get-BuildFile {
    if ($Kts) { $b = 'build.gradle.kts'; $o = 'build.gradle' } else { $b = 'build.gradle'; $o = 'build.gradle.kts' }
    if (-not (Test-Path -LiteralPath (Get-ProjectPath $b) -PathType Leaf) -and (Test-Path -LiteralPath (Get-ProjectPath $o) -PathType Leaf)) { $b = $o }
    return $b
}

# Test-Coordinate tells whether the root build file or gradle.properties sets Name.
function Test-Coordinate([string]$Name) {
    foreach ($f in @((Get-BuildFile), 'gradle.properties')) {
        $p = Get-ProjectPath $f
        if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { continue }
        foreach ($l in [IO.File]::ReadAllLines($p)) { if ($l -cmatch "^\s*$Name\s*=") { return $true } }
    }
    return $false
}

$Counter = @{ N = 1 }
function Write-Step([string]$Text) { Write-Host "  $($Counter.N). $Text"; $Counter.N++ }
function Write-More([string]$Text) { Write-Host "     $Text" }

if ($Tool -eq 'gradle') {
    $run = (Get-GradleCommand) + ' publish'
    if ($run -like './*' -and [IO.Path]::DirectorySeparatorChar -eq '\') { $run = '.\gradlew publish' }
} else {
    $run = 'mvn deploy'
}
if ($New.Count + $Edit.Count -gt 0) {
    if ($Manual.Count -gt 0) { Write-Host 'Almost done: paste the blocks above, then commit. Changed files:' } else { Write-Host 'Done. Changed files:' }
    foreach ($name in @($New) + @($Edit)) { Write-Host "  $name" }
    Write-Host ''
    Write-Host 'Next:'
    Write-Step 'Commit these files.'
} else {
    Write-Host 'Done. The project is already set up. No file changed.'
    Write-Host ''
    Write-Host 'Next:'
    Write-Step 'Commit the files if you have not yet.'
}
if ($Tool -eq 'gradle' -and -not ((Test-Coordinate 'group') -and (Test-Coordinate 'version'))) {
    Write-Step "Set group and version in $(Get-BuildFile), for example group = `"com.example`" and version = `"1.0.0`"."
}
Write-Step "Run $run and click Approve in your browser the first time."
if ($Ci) { Write-Step 'Ask an admin to add a CI pipeline on Settings › CI pipelines.' }
switch ($Ci) {
    'github' { Write-More 'The CI pipeline must allow tags v*. The workflow publishes when you push a tag such as v1.0.0.' }
    'jenkins-pipeline' { Write-Step $JenkinsNote }
    'jenkins-freestyle' {
        Write-Step $JenkinsNote
        if ($Tool -eq 'gradle') { $shell = (Get-GradleCommand) + ' publish' } else { $shell = 'mvn -B deploy -Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN' }
        Write-Step 'Set up the freestyle job in Jenkins:'
        Write-More 'a. Open the job, click Configure.'
        Write-More 'b. Under Environment, tick "Use secret text(s) or file(s)".'
        Write-More 'c. Under Bindings, click Add, then "Secret text".'
        Write-More "d. Set Variable OIDC_TOKEN and Credentials $Credential."
        Write-More 'e. Click "Add build step", then "Execute shell" ("Execute Windows batch command" on Windows agents).'
        Write-More "f. Type: $shell"
        Write-More 'g. Click Save, then Build Now.'
    }
}
if ($Manual.Count -gt 0) { throw 'telesphorepo setup: paste the blocks above by hand' }
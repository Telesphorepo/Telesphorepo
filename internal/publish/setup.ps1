# Telesphorepo project setup. Run it in the folder of a project (@@PROJECTS@@):
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
$Credential = '@@CREDENTIAL@@'
$JenkinsNote = '@@JENKINS_NOTE@@'
$Tools = @(@@TOOLS@@)

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

Run it in the folder with @@PROJECTS@@.
"@
    return
}

$Interactive = [Environment]::UserInteractive -and -not [Console]::IsInputRedirected
$Here = (Get-Location).ProviderPath
function Get-ProjectPath([string]$Name) { return [IO.Path]::Combine($Here, $Name.Replace('/', [IO.Path]::DirectorySeparatorChar)) }
function Test-ProjectFile([string]$Name) { return (Test-Path -LiteralPath (Get-ProjectPath $Name) -PathType Leaf) }

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

# Find-OnlyLine returns the line (1-based) that holds only Tag when Tag is on one line of the file;
# else 0.
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

$Counter = @{ N = 1 }
function Write-Step([string]$Text) { Write-Host "  $($Counter.N). $Text"; $Counter.N++ }
function Write-More([string]$Text) { Write-Host "     $Text" }

# ---------------------------------------------------------------- the build tools

@@FRAGMENTS@@

# ---------------------------------------------------------------- the CI files

$CiFiles = @{}
@@CI_FILES@@

# ---------------------------------------------------------------- the build tool

function Get-ToolName([string]$Id) { return $Id.Substring(0, 1).ToUpperInvariant() + $Id.Substring(1) }

$Tool = ''
foreach ($t in $Tools) {
    if (& ('Test-' + (Get-ToolName $t))) { $Tool = $t; break }
}
if (-not $Tool) { Stop-Setup 'this folder has no @@PROJECTS@@. Run the command in the folder of your project.' }
$ToolName = Get-ToolName $Tool

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

# ---------------------------------------------------------------- CI

# Get-CiText returns the CI file Kind (github, jenkins, freestyle) of the build tool for the repo.
function Get-CiText([string]$Kind) {
    $text = $CiFiles[$Tool][$Kind].Replace("`r`n", "`n").TrimEnd("`n").Replace('%REPO%', $Repo)
    return (& ('Edit-' + $ToolName + 'Ci') $text)
}

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
    switch ($Ci) {
        'github' { Set-CiFile '.github/workflows/publish.yml' (Get-CiText 'github') }
        'jenkins-pipeline' { Set-CiFile 'Jenkinsfile' (Get-CiText 'jenkins') }
    }
}

# ---------------------------------------------------------------- run

& ('Set-' + $ToolName)
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
    Write-Host "Changes to the project to publish to $Repo on ${Url}:"
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
& ('Write-' + $ToolName + 'Next')
$run = & ('Get-' + $ToolName + 'PublishCommand')
Write-Step "Run $run and click Approve in your browser the first time."
if ($Ci) { Write-Step 'Ask an admin to add a CI pipeline on Settings › CI pipelines.' }
switch ($Ci) {
    'github' { Write-More 'The CI pipeline must allow tags v*. The workflow publishes when you push a tag such as v1.0.0.' }
    'jenkins-pipeline' { Write-Step $JenkinsNote }
    'jenkins-freestyle' {
        Write-Step $JenkinsNote
        Write-Step 'Set up the freestyle job in Jenkins:'
        Write-More 'a. Open the job, click Configure.'
        Write-More 'b. Under Environment, tick "Use secret text(s) or file(s)".'
        Write-More 'c. Under Bindings, click Add, then "Secret text".'
        Write-More "d. Set Variable OIDC_TOKEN and Credentials $Credential."
        Write-More 'e. Click "Add build step", then "Execute shell" ("Execute Windows batch command" on Windows agents).'
        Write-More "f. Type: $(Get-CiText 'freestyle')"
        Write-More 'g. Click Save, then Build Now.'
    }
}
if ($Manual.Count -gt 0) { throw 'telesphorepo setup: paste the blocks above by hand' }

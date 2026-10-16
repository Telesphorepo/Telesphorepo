# Gradle: the Telesphorepo plugin in the settings file.
$Version = '@@VERSION@@'
$GradlePluginId = 'com.telesphorepo.publish'

function Test-Gradle {
    if (Test-ProjectFile 'settings.gradle.kts') {
        $script:GradleSettings = 'settings.gradle.kts'; $script:GradleKts = $true
    } elseif (Test-ProjectFile 'settings.gradle') {
        $script:GradleSettings = 'settings.gradle'; $script:GradleKts = $false
    } else {
        return $false
    }
    return $true
}

function Get-GradleCommand {
    if (Test-ProjectFile 'gradlew') { return './gradlew' }
    return 'gradle'
}

function Get-GradlePublishCommand {
    $run = (Get-GradleCommand) + ' publish'
    if ($run -like './*' -and [IO.Path]::DirectorySeparatorChar -eq '\') { $run = '.\gradlew publish' }
    return $run
}

# Edit-GradleCi runs a CI file through: a project without the wrapper runs gradle.
function Edit-GradleCi([string]$Text) {
    if (Test-ProjectFile 'gradlew') { return $Text }
    return $Text.Replace('./gradlew', 'gradle')
}

function Set-Gradle {
    $f = $GradleSettings
    $c = Get-Work $f
    if ($GradleKts) {
        $idLine = "id(`"$GradlePluginId`") version `"$Version`""
        $block = "telesphorepo {`n    url = `"$Url`"`n    repository = `"$Repo`""
        $oidc = '    oidcTokenEnv = "OIDC_TOKEN"'
    } else {
        $idLine = "id '$GradlePluginId' version '$Version'"
        $block = "telesphorepo {`n    url = '$Url'`n    repository = '$Repo'"
        $oidc = "    oidcTokenEnv = 'OIDC_TOKEN'"
    }
    if ($Ci -like 'jenkins-*') { $block += "`n$oidc" }
    $block += "`n}"
    $plugins = "plugins {`n    $idLine`n}"

    if (-not (Get-Text $c).Contains($GradlePluginId)) {
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

# Get-GradleBuildFile returns the root build file of the Gradle project.
function Get-GradleBuildFile {
    if ($GradleKts) { $b = 'build.gradle.kts'; $o = 'build.gradle' } else { $b = 'build.gradle'; $o = 'build.gradle.kts' }
    if (-not (Test-ProjectFile $b) -and (Test-ProjectFile $o)) { $b = $o }
    return $b
}

# Test-GradleSetting tells whether the root build file or gradle.properties sets Name.
function Test-GradleSetting([string]$Name) {
    foreach ($f in @((Get-GradleBuildFile), 'gradle.properties')) {
        $p = Get-ProjectPath $f
        if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { continue }
        foreach ($l in [IO.File]::ReadAllLines($p)) { if ($l -cmatch "^\s*$Name\s*=") { return $true } }
    }
    return $false
}

function Write-GradleNext {
    if (-not ((Test-GradleSetting 'group') -and (Test-GradleSetting 'version'))) {
        Write-Step "Set group and version in $(Get-GradleBuildFile), for example group = `"com.example`" and version = `"1.0.0`"."
    }
}

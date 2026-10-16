# Maven: the Telesphorepo extension, the server address and distributionManagement.
$Version = '@@VERSION@@'
$MavenExtensionId = 'telesphorepo-maven-extension'

function Test-Maven { return (Test-ProjectFile 'pom.xml') }

function Get-MavenPublishCommand { return 'mvn deploy' }

function Edit-MavenCi([string]$Text) { return $Text }

function Write-MavenNext { }

function Set-Maven {
    $f = '.mvn/extensions.xml'
    $ext = "  <extension>`n    <groupId>com.telesphorepo</groupId>`n    <artifactId>$MavenExtensionId</artifactId>`n    <version>$Version</version>`n  </extension>"
    $c = Get-Work $f
    if (-not $c.Exists) {
        Add-Lines $c 0 "<extensions>`n$ext`n</extensions>"
        Add-Plan $f 'create it' (Get-Text $c)
    } elseif (-not (Get-Text $c).Contains($MavenExtensionId)) {
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

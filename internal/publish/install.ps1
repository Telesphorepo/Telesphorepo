# Installs the Telesphorepo CLI of the server @@URL@@ with PowerShell.
#
#   irm @@URL@@/install.ps1 | iex
#   & ([scriptblock]::Create((irm @@URL@@/install.ps1))) setup 'LINK'
#
# The script downloads the archive of this OS and CPU from the server, checks its SHA-256, and
# installs telesphorepo into %LOCALAPPDATA%\Programs\telesphorepo (on Linux and macOS:
# ~/.local/bin) with docker-credential-telesphorepo next to it. It puts the folder on the PATH of
# this session; on Windows it adds the folder to the user PATH too, and on Linux and macOS it
# prints the line for the PowerShell profile when the folder is not on PATH. In GitHub Actions it adds the folder to the PATH of the later steps. Arguments
# after the script go to the CLI, which runs once the install is done.

$Url = '@@URL@@'
$Sums = @{
@@SUMS@@
}

function Install-Telesphorepo {
	param([string[]]$CliArgs)
	$ErrorActionPreference = 'Stop'
	$ProgressPreference = 'SilentlyContinue'

	$onWindows = -not ($IsLinux -or $IsMacOS)
	if ($IsLinux) { $os = 'linux' } elseif ($IsMacOS) { $os = 'darwin' } else { $os = 'windows' }
	$cpu = ''
	try { $cpu = [string][System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch { }
	if (-not $cpu) { $cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE } }
	switch -regex ($cpu) {
		'^(X64|AMD64)$' { $arch = 'amd64' }
		'^(Arm64|ARM64)$' { $arch = 'arm64' }
		default { throw "telesphorepo: the server has no CLI for the CPU $cpu" }
	}
	if ($onWindows) { $name = "telesphorepo_${os}_${arch}.zip" } else { $name = "telesphorepo_${os}_${arch}.tar.gz" }
	$want = $Sums[$name]
	if (-not $want) { throw "telesphorepo: the server has no CLI for $os/$arch" }

	$tmp = Join-Path ([IO.Path]::GetTempPath()) ('telesphorepo-' + [guid]::NewGuid())
	New-Item -ItemType Directory -Path $tmp | Out-Null
	try {
		$file = Join-Path $tmp $name
		try {
			Invoke-WebRequest -UseBasicParsing -Uri "$Url/cli/$name" -OutFile $file
		} catch {
			throw "telesphorepo: the download of $Url/cli/$name failed: $($_.Exception.Message)"
		}
		$got = (Get-FileHash -Algorithm SHA256 -Path $file).Hash.ToLowerInvariant()
		if ($got -ne $want) { throw "telesphorepo: the SHA-256 of $name is $got, but the server names $want; nothing was installed" }

		if ($onWindows) {
			Expand-Archive -Path $file -DestinationPath $tmp -Force
			$dir = Join-Path $env:LOCALAPPDATA 'Programs\telesphorepo'
			$exeName = 'telesphorepo.exe'
			$helperName = 'docker-credential-telesphorepo.exe'
		} else {
			& tar -xzf $file -C $tmp telesphorepo
			if ($LASTEXITCODE -ne 0) { throw "telesphorepo: $name does not hold telesphorepo" }
			$dir = Join-Path $HOME '.local/bin'
			$exeName = 'telesphorepo'
			$helperName = 'docker-credential-telesphorepo'
		}
		New-Item -ItemType Directory -Force -Path $dir | Out-Null
		$exe = Join-Path $dir $exeName
		$helper = Join-Path $dir $helperName
		# A running program on Windows cannot be replaced, but it can be renamed. The CLI deletes
		# the .old file at its next start.
		foreach ($old in @($exe, $helper)) {
			if (Test-Path -LiteralPath $old) {
				try { Remove-Item -LiteralPath $old -Force } catch { Move-Item -LiteralPath $old -Destination "$old.old" -Force }
			}
		}
		Move-Item -LiteralPath (Join-Path $tmp $exeName) -Destination $exe -Force
		if ($onWindows) {
			try { New-Item -ItemType HardLink -Path $helper -Target $exe | Out-Null } catch { Copy-Item -LiteralPath $exe -Destination $helper }
		} else {
			& chmod 755 $exe
			try { New-Item -ItemType SymbolicLink -Path $helper -Target $exeName | Out-Null } catch { Copy-Item -LiteralPath $exe -Destination $helper }
		}
	} finally {
		Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
	}
	[Console]::Error.WriteLine("telesphorepo: installed the CLI of $Url in $dir")

	$sep = [IO.Path]::PathSeparator
	$onPath = ($env:PATH -split [regex]::Escape($sep)) -contains $dir
	if ($onWindows) {
		$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
		if (-not (($userPath -split ';') -contains $dir)) {
			$newPath = if ($userPath) { "$userPath;$dir" } else { $dir }
			[Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
			[Console]::Error.WriteLine("telesphorepo: added $dir to your user PATH. This PowerShell session can run telesphorepo now; open a new terminal to run it anywhere else.")
		}
	} elseif (-not $onPath -and -not $env:GITHUB_PATH) {
		[Console]::Error.WriteLine("telesphorepo: ~/.local/bin is not on PATH. This PowerShell session can run telesphorepo now; for new terminals, add this line to your PowerShell profile ($PROFILE):")
		[Console]::Error.WriteLine('  $env:PATH = "$HOME/.local/bin:$env:PATH"')
	}
	if (-not $onPath) { $env:PATH = "$dir$sep$env:PATH" }
	if ($env:GITHUB_PATH) { Add-Content -LiteralPath $env:GITHUB_PATH -Value $dir }

	if ($CliArgs -and $CliArgs.Count -gt 0) {
		& $exe @CliArgs
	}
}

Install-Telesphorepo -CliArgs $args

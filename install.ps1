# Install a checksum-verified Windows release from RedwindA/ccusage_go.
#   irm https://github.com/RedwindA/ccusage_go/releases/latest/download/install.ps1 | iex
# A specific version:
#   & ([scriptblock]::Create((irm https://github.com/RedwindA/ccusage_go/releases/latest/download/install.ps1))) v0.18.0
# Set INSTALL_DIR to override %LOCALAPPDATA%\Programs\ccusage_go; VERSION may also select a release.
# Set CCUSAGE_GO_NO_MODIFY_PATH=1 to leave the user PATH unchanged.

# The script block keeps variables and preferences out of the caller's
# session when run through iex; errors throw instead of exiting its shell.
& {
param([string]$Version)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # Windows PowerShell downloads crawl with a progress bar

if ($Version -in '-h', '--help', '-?', '/?') {
  'Usage: install.ps1 [vVERSION]'
  'Defaults to the latest stable release.'
  'Set INSTALL_DIR to override %LOCALAPPDATA%\Programs\ccusage_go; VERSION may also select a release.'
  'Set CCUSAGE_GO_NO_MODIFY_PATH=1 to leave the user PATH unchanged.'
  return
}
if (-not $Version) { $Version = $env:VERSION }
if (-not $Version) { $Version = 'latest' }
if ($Version -ne 'latest' -and $Version -cnotmatch '^v[0-9][0-9A-Za-z._-]*$') {
  throw 'Use a version tag such as v0.18.0, or latest.'
}
if ($env:OS -ne 'Windows_NT') {
  throw 'install.ps1 installs the Windows build; on Linux and macOS use install.sh.'
}

$osArch = $null
try { $osArch = [string][System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch {}
if (-not $osArch) {
  $osArch = $env:PROCESSOR_ARCHITEW6432
  if (-not $osArch) { $osArch = $env:PROCESSOR_ARCHITECTURE }
}
switch ($osArch) {
  { $_ -in 'X64', 'AMD64' } { $arch = 'amd64'; break }
  'Arm64' { $arch = 'arm64'; break }
  default { throw "Supported architectures: x64 and arm64 (found $osArch)." }
}

# Windows PowerShell 5.1 may not offer TLS 1.2 by default.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$installDir = $env:INSTALL_DIR
if (-not $installDir) {
  if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set; set INSTALL_DIR.' }
  $installDir = Join-Path $env:LOCALAPPDATA 'Programs\ccusage_go'
}
# Resolve against the PowerShell location; .NET calls use the process directory.
$installDir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($installDir).TrimEnd('\', '/')
$target = Join-Path $installDir 'ccusage_go.exe'
if (Test-Path -LiteralPath $target -PathType Container) { throw 'Install destination is a directory.' }

$repoUrl = 'https://github.com/RedwindA/ccusage_go'
if ($Version -eq 'latest') {
  # Resolve once so the archive and checksum always come from the same release.
  $base = (Invoke-WebRequest -Uri "$repoUrl/releases/latest" -Method Head -UseBasicParsing).BaseResponse
  if ($base.ResponseUri) { $resolved = $base.ResponseUri.AbsoluteUri } # Windows PowerShell
  else { $resolved = $base.RequestMessage.RequestUri.AbsoluteUri }      # PowerShell 7
  $prefix = "$repoUrl/releases/tag/"
  if (-not "$resolved".StartsWith($prefix)) { throw 'Could not resolve the latest release.' }
  $Version = "$resolved".Substring($prefix.Length)
  if ($Version -cnotmatch '^v[0-9][0-9A-Za-z._-]*$') { throw 'Invalid release tag.' }
}

$binary = "ccusage_go-windows-$arch.exe"
$archive = "$binary.zip"
$baseUrl = "$repoUrl/releases/download/$Version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ('ccusage_go-' + [Guid]::NewGuid().ToString('N'))
$staged = $null
$zip = Join-Path $tmp $archive
$sums = Join-Path $tmp 'checksums.txt'
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  "Downloading ccusage_go $Version (windows/$arch)..."
  Invoke-WebRequest -Uri "$baseUrl/$archive" -OutFile $zip -UseBasicParsing
  Invoke-WebRequest -Uri "$baseUrl/checksums.txt" -OutFile $sums -UseBasicParsing
  $expected = $null
  foreach ($line in Get-Content -LiteralPath $sums) {
    $fields = -split $line
    if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -ceq $archive) { $expected = $fields[0]; break }
  }
  if ($expected -notmatch '^[0-9a-fA-F]{64}$') { throw "Missing or invalid checksum for $archive." }
  # Hash and unzip through .NET: Get-FileHash and Expand-Archive live in
  # script modules that Windows PowerShell cannot load when started with
  # PowerShell 7's PSModulePath.
  $sha = [Security.Cryptography.SHA256]::Create()
  $stream = [IO.File]::OpenRead($zip)
  try { $actual = -join ($sha.ComputeHash($stream) | ForEach-Object { $_.ToString('x2') }) }
  finally { $stream.Dispose(); $sha.Dispose() }
  if ($actual -ne $expected) { throw 'Checksum mismatch; installation aborted.' }

  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $zipFile = [IO.Compression.ZipFile]::OpenRead($zip)
  try {
    $entry = $zipFile.GetEntry($binary)
    if (-not $entry) { throw "$archive does not contain $binary." }
    if ($entry.Length -eq 0) { throw 'Archive contained an empty binary.' }
    # Stage beside the target so the final step is a rename on the same volume.
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    $staged = Join-Path $installDir ('.ccusage_go-install-' + [Guid]::NewGuid().ToString('N') + '.exe')
    [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $staged)
  } finally {
    $zipFile.Dispose()
  }
  if (Test-Path -LiteralPath $target) {
    # A running ccusage_go.exe cannot be overwritten but can be renamed; the
    # aside copy is removed the next time ccusage_go starts. A copy still
    # held by a running process gets a fresh name instead.
    Remove-Item -LiteralPath "$target.old" -Force -ErrorAction SilentlyContinue
    $aside = "$target.old"
    if (Test-Path -LiteralPath $aside) { $aside = "$target.old-" + [Guid]::NewGuid().ToString('N') }
    [IO.File]::Move($target, $aside)
    try { [IO.File]::Move($staged, $target) }
    catch { [IO.File]::Move($aside, $target); throw }
    Remove-Item -LiteralPath $aside -Force -ErrorAction SilentlyContinue
  } else {
    [IO.File]::Move($staged, $target)
  }
  $staged = $null
} finally {
  if ($staged) { Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue }
  Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
"Installed $target ($Version)"

$inSession = @($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq $installDir })
if ($env:CCUSAGE_GO_NO_MODIFY_PATH -eq '1') {
  if (-not $inSession) { "Add this directory to your PATH: $installDir" }
} else {
  # Edit the raw registry value: [Environment]::SetEnvironmentVariable would
  # rewrite it as REG_SZ and freeze entries such as %USERPROFILE%\bin.
  $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
  try {
    $userPath = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    $entries = @($userPath -split ';' | Where-Object { $_ })
    $listed = $entries | Where-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ieq $installDir }
    if (-not $listed) {
      $key.SetValue('Path', (@($installDir) + $entries) -join ';', [Microsoft.Win32.RegistryValueKind]::ExpandString)
      # Setting any user variable broadcasts WM_SETTINGCHANGE, so terminals
      # opened from now on see the new PATH.
      [Environment]::SetEnvironmentVariable('CCUSAGE_GO_INSTALL', '1', 'User')
      [Environment]::SetEnvironmentVariable('CCUSAGE_GO_INSTALL', $null, 'User')
      "Added $installDir to your user PATH."
    }
  } finally {
    $key.Dispose()
  }
  if (-not $inSession) {
    $env:Path = "$installDir;$env:Path"
    'Open a new terminal if ccusage_go is not found.'
  }
}
$onPath = @($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq $installDir })
$found = Get-Command ccusage_go -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($onPath -and $found -and $found.Source -ne $target) {
  "Warning: $($found.Source) comes first on PATH; remove it to use the new install."
}
} @args

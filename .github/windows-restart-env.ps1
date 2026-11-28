$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath($env:GITHUB_WORKSPACE)
function Assert-ProjectPath([string]$path) {
  $full = [IO.Path]::GetFullPath($path)
  if (-not $full.StartsWith($projectRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw "Output escapes project: $full" }
  $current = $full
  while ($current) {
    if (Test-Path -LiteralPath $current) {
      if ((Get-Item -Force -LiteralPath $current).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Reparse point: $current" }
    }
    $parent = [IO.Path]::GetDirectoryName($current)
    if ($parent -eq $current) { break }; $current = $parent
  }
}
$outputs = @{
 TEMP='.cache/tmp'; TMP='.cache/tmp'; TMPDIR='.cache/tmp'; GOTMPDIR='.cache/tmp'
 GOCACHE='.cache/go-build'; GOMODCACHE='.cache/go-mod'; GOPATH='.cache/go-path'; GOBIN='.cache/go-path/bin'
 USERPROFILE='.cache/profile'; APPDATA='.cache/profile/appdata'; LOCALAPPDATA='.cache/profile/local'
 PROJECT_TOOL_CACHE='.cache/tools'
}
foreach ($entry in $outputs.GetEnumerator()) {
 $path=Join-Path $projectRoot $entry.Value
 Assert-ProjectPath $path
 New-Item -ItemType Directory -Force -Path $path | Out-Null
 [Environment]::SetEnvironmentVariable($entry.Key,$path,'Process')
 "$($entry.Key)=$path" | Out-File -Encoding utf8 -Append $env:GITHUB_ENV
}
foreach ($path in @('.cache/evidence','.cache/baseline','.cache/bin')) { Assert-ProjectPath (Join-Path $projectRoot $path); New-Item -ItemType Directory -Force (Join-Path $projectRoot $path) | Out-Null }
$archive=Join-Path $env:PROJECT_TOOL_CACHE 'go1.26.3.windows-amd64.zip'
Invoke-WebRequest 'https://go.dev/dl/go1.26.3.windows-amd64.zip' -OutFile $archive
if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne '20d2ceafb4ed41b96b879010927b28bc92a5be57a7c1801ce365a9ca51d3224a') { throw 'Go checksum mismatch' }
Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip=[IO.Compression.ZipFile]::OpenRead($archive)
try { foreach ($entry in $zip.Entries) {
 if (-not $entry.FullName.StartsWith('go/') -or $entry.FullName -match '(^|/)\.\.(/|$)' -or (($entry.ExternalAttributes -shr 16) -band 0xF000) -eq 0xA000) { throw "Unsafe archive member: $($entry.FullName)" }
 Assert-ProjectPath (Join-Path $env:PROJECT_TOOL_CACHE $entry.FullName)
} } finally { $zip.Dispose() }
Expand-Archive -LiteralPath $archive -DestinationPath $env:PROJECT_TOOL_CACHE
(Join-Path $env:PROJECT_TOOL_CACHE 'go/bin') | Out-File -Encoding utf8 -Append $env:GITHUB_PATH
Write-Host 'Validated project-local Windows outputs and installed checksum-pinned Go.'

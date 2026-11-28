$ErrorActionPreference='Stop'
$Label=$args[0]
$Command=$args[1..($args.Length-1)]
$log=Join-Path $env:GITHUB_WORKSPACE ".cache/evidence/$Label.log"
$program=$Command[0]; $arguments=$Command[1..($Command.Length-1)]
& $program @arguments 2>&1 | Tee-Object -FilePath $log
$code=$LASTEXITCODE
if ($code -ne 0) {
 $lines=Get-Content $log
 $indices=@()
 for ($i=0; $i -lt $lines.Count; $i++) { if ($lines[$i] -match '--- FAIL:|panic:|fatal error:|WARNING: DATA RACE') { $indices+=$i } }
 if ($indices.Count -eq 0) { $indices=@([Math]::Max(0,$lines.Count-100)) }
 foreach ($i in ($indices | Select-Object -First 20)) {
  $end=[Math]::Min($lines.Count-1,$i+60)
  $text=($lines[$i..$end]) -join "`n"
  if ($text.Length -gt 14000) { $text=$text.Substring(0,14000) }
  $text=$text.Replace('%','%25').Replace("`r",'%0D').Replace("`n",'%0A')
  Write-Host "::error title=${Label}::$text"
 }
 exit $code
}

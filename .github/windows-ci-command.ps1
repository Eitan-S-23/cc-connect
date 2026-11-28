param([string]$Label, [Parameter(ValueFromRemainingArguments=$true)][string[]]$Command)
$ErrorActionPreference='Stop'
$log=Join-Path $env:GITHUB_WORKSPACE ".cache/evidence/$Label.log"
$program=$Command[0]; $arguments=$Command[1..($Command.Length-1)]
& $program @arguments 2>&1 | Tee-Object -FilePath $log
$code=$LASTEXITCODE
if ($code -ne 0) {
 $lines=Get-Content $log
 $text=($lines | Select-Object -Last 100) -join "`n"
 $text=$text.Replace('%','%25').Replace("`r",'%0D').Replace("`n",'%0A')
 Write-Host "::error title=${Label}::$text"
 exit $code
}

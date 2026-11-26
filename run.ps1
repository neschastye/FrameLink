$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
if (!(Test-Path '.tools/go/bin/go.exe') -or !(Test-Path 'bundle/mkcert.bin')) { & ./setup.ps1 }
$env:GOTOOLCHAIN = 'local'
$env:GOTELEMETRY = 'off'
& .tools/go/bin/go.exe run . @args
exit $LASTEXITCODE

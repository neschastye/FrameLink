$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
$go = '.tools/go/bin/go.exe'
if (!(Test-Path $go) -or !(Test-Path 'bundle/mkcert.bin')) { & ./setup.ps1 }
if ((Get-FileHash 'bundle/mkcert.bin').Hash -ne (Get-Content -Raw 'bundle/mkcert.sha256').Trim()) { throw 'Run setup.ps1 to restore mkcert.' }
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
$env:GOTOOLCHAIN = 'local'
$env:GOTELEMETRY = 'off'
& $go vet ./...
if ($LASTEXITCODE) { throw 'go vet failed' }
& $go test ./...
if ($LASTEXITCODE) { throw 'go test failed' }
New-Item -Type Directory -Force dist | Out-Null
& $go build -mod=readonly -trimpath -buildvcs=false -ldflags '-s -w' -o dist/FrameLink.exe .
if ($LASTEXITCODE) { throw 'Build failed' }
Get-FileHash dist/FrameLink.exe

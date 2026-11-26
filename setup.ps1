# Project-local toolchain. No PATH, registry, trust-store or firewall changes.
$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
$toolDir = Join-Path $PSScriptRoot '.tools'
New-Item -Type Directory -Force $toolDir, 'bundle' | Out-Null
function Get-VerifiedFile($Url, $Out, $Digest) {
    if ((Test-Path -LiteralPath $Out) -and (Get-FileHash -LiteralPath $Out).Hash -eq $Digest) { return }
    $tempFile = "$Out.download"
    & curl.exe -fsSL --retry 3 --output $tempFile $Url
    if ($LASTEXITCODE -ne 0) { throw "Download failed: $Url" }
    if ((Get-FileHash -LiteralPath $tempFile).Hash -ne $Digest) { throw "Checksum mismatch for $Url" }
    Move-Item -Force -LiteralPath $tempFile -Destination $Out
}
$go = Join-Path $toolDir 'go/bin/go.exe'
if (!(Test-Path $go) -or ((& $go version) -notmatch 'go1\.27\.4 windows/amd64')) {
    $archive = Join-Path $toolDir 'go.zip'
    Get-VerifiedFile 'https://dl.google.com/go/go1.27.4.windows-amd64.zip' $archive '178ab98950ac69ab25069307dc6ade79f3816e414cce95b4da166a115cde1e7e'
    & tar.exe -xf $archive -C $toolDir
    if ($LASTEXITCODE) { throw 'Could not extract Go' }
}
$env:GOTOOLCHAIN = 'local'
$env:GOTELEMETRY = 'off'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:GOAMD64 = 'v1'
$env:CGO_ENABLED = '0'
$env:GOWORK = 'off'
$env:GOFLAGS = ''
$env:GOEXPERIMENT = ''
$digest = (Get-Content -Raw 'bundle/mkcert.sha256').Trim()
if (!(Test-Path 'bundle/mkcert.bin') -or (Get-FileHash 'bundle/mkcert.bin').Hash -ne $digest) {
    & $go -C tools/mkcert mod download
    if ($LASTEXITCODE) { throw 'mkcert dependency download failed' }
    & $go -C tools/mkcert mod verify
    if ($LASTEXITCODE) { throw 'mkcert dependency verification failed' }
    & $go -C tools/mkcert build -mod=readonly -trimpath -buildvcs=false -ldflags '-s -w -X main.Version=v1.4.4' -o ../../bundle/mkcert.bin filippo.io/mkcert
    if ($LASTEXITCODE) { throw 'mkcert build failed' }
    if ((Get-FileHash 'bundle/mkcert.bin').Hash -ne $digest) { throw 'mkcert build checksum mismatch' }
}
& $go mod download
if ($LASTEXITCODE) { throw 'go mod download failed' }
& $go mod verify
if ($LASTEXITCODE) { throw 'go mod verify failed' }

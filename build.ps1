$env:CGO_ENABLED = "0"
$env:GOFLAGS = "-trimpath"

# linux/arm64 (Keenetic Giga KN-1010, Ultra KN-1811 — aarch64)
$env:GOOS = "linux"; $env:GOARCH = "arm64"
go build -ldflags="-s -w" -o dist/doq-web-linux-arm64 .
if ($LASTEXITCODE -ne 0) { Write-Host "FAIL arm64"; exit 1 }
Write-Host "OK  dist/doq-web-linux-arm64"

# linux/arm GOARM=7 (Keenetic old models — armv7sf-k3.2)
$env:GOARCH = "arm"; $env:GOARM = "7"
go build -ldflags="-s -w" -o dist/doq-web-linux-arm .
if ($LASTEXITCODE -ne 0) { Write-Host "FAIL arm"; exit 1 }
Write-Host "OK  dist/doq-web-linux-arm"

# linux/mipsle softfloat (Keenetic Ultra KN-1810, Hero KN-1011 — mipselsf-k3.4)
$env:GOARCH = "mipsle"; $env:GOMIPS = "softfloat"
go build -ldflags="-s -w" -o dist/doq-web-linux-mipsle .
if ($LASTEXITCODE -ne 0) { Write-Host "FAIL mipsle"; exit 1 }
Write-Host "OK  dist/doq-web-linux-mipsle"

# windows/amd64
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:GOMIPS = ""; $env:GOARM = ""
go build -ldflags="-s -w" -o dist/doq-web-windows-amd64.exe .
if ($LASTEXITCODE -ne 0) { Write-Host "FAIL win"; exit 1 }
Write-Host "OK  dist/doq-web-windows-amd64.exe"

Write-Host ""
Get-ChildItem dist/ | ForEach-Object {
    Write-Host ("  {0,-30} {1:N0} bytes" -f $_.Name, $_.Length)
}

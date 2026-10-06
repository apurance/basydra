param([switch]$SkipTests)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$goTool = (Get-Command go -CommandType Application -ErrorAction Stop).Source
$previous = @{}
foreach ($name in @('GOOS','GOARCH','CGO_ENABLED','GOTMPDIR','TEMP','TMP')) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
Push-Location $repo
try {
    New-Item -ItemType Directory -Force -Path bin,.local/tmp | Out-Null
    $env:GOTMPDIR = Join-Path $repo '.local/tmp'
    $env:TEMP = $env:GOTMPDIR
    $env:TMP = $env:GOTMPDIR
    $env:CGO_ENABLED = '0'
    Remove-Item Env:GOOS,Env:GOARCH -ErrorAction SilentlyContinue
    if (-not $SkipTests) {
        & $goTool test ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
        & $goTool vet ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go vet failed' }
    }
    & $goTool build -trimpath -ldflags '-s -w' -o bin/basydra.exe ./cmd/basydra
    if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
    $env:GOOS = 'linux'
    foreach ($architecture in @('amd64','arm64')) {
        $env:GOARCH = $architecture
        & $goTool build -trimpath -ldflags '-s -w' -o "bin/basydra-linux-$architecture" ./cmd/basydra
        if ($LASTEXITCODE -ne 0) { throw "Linux $architecture build failed" }
    }
    Get-ChildItem bin/basydra* | Select-Object Name,Length
} finally {
    foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name,$previous[$name],'Process') }
    Pop-Location
}

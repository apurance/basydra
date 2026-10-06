param(
    [Parameter(Mandatory=$true)][ValidatePattern('^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,199}$')][string]$Target,
    [Parameter(Mandatory=$true)][string]$TokenFile,
    [ValidateSet('amd64','arm64')][string]$Architecture = 'amd64'
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$binary = Join-Path $repo "bin/basydra-linux-$Architecture"
$unit = Join-Path $repo 'deploy/basydra-agent.service'
if (-not (Test-Path -LiteralPath $binary)) { throw 'Build first with scripts/build.ps1' }
if (-not (Test-Path -LiteralPath $TokenFile)) { throw 'Token file does not exist' }
$token = (Get-Content -LiteralPath $TokenFile -Raw).Trim()
if ($token.Length -lt 24 -or $token.Contains("`n") -or $token.Contains("`r")) { throw 'Invalid token' }
$tokenName = Split-Path -Leaf $TokenFile
if ($tokenName -notmatch '^[A-Za-z0-9_.-]+$') { throw 'Token file name must contain only letters, digits, dot, dash or underscore' }
$sshOptions = @('-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','ConnectTimeout=10')
& ssh @sshOptions $Target 'set -eu; sudo -n true; test ! -e /etc/basydra/agent.token; test ! -e /etc/systemd/system/basydra-agent.service; test ! -e /opt/basydra; mkdir -p -m 700 "$HOME/basydra-stage"'
if ($LASTEXITCODE -ne 0) { throw 'Preflight failed: requires sudo and an unused Basydra installation (existing deployments are never overwritten)' }
& scp @sshOptions $binary $unit $TokenFile "${Target}:basydra-stage/"
if ($LASTEXITCODE -ne 0) { throw 'Upload failed' }
$command = @'
set -eu
cd "$HOME/basydra-stage"
sudo install -d -m 755 /opt/basydra
sudo install -d -m 700 /etc/basydra
sudo install -m 755 BINARY /opt/basydra/basydra
sudo install -m 600 TOKEN /etc/basydra/agent.token
sudo install -m 644 basydra-agent.service /etc/systemd/system/basydra-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now basydra-agent.service
rm -f TOKEN
sudo systemctl is-active basydra-agent.service
'@
$command = $command.Replace('BINARY',"basydra-linux-$Architecture").Replace('TOKEN',$tokenName).Replace("`r",'')
$encodedCommand = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($command))
& ssh @sshOptions $Target "printf %s $encodedCommand | base64 -d | sh -s"
if ($LASTEXITCODE -ne 0) { throw 'Agent deployment failed; inspect sudo journalctl -u basydra-agent' }
Write-Output "Agent deployed on $Target at remote loopback 127.0.0.1:19790"

# Start everything the character needs: Laya (decisions), the voice service (Whisper + Piper +
# Py4J to the mod) and the agent. Start Minecraft and enter a world first.
#   powershell -ExecutionPolicy Bypass -File scripts\start-all.ps1
# Where things live: LAYA_SERVE (laya-serve.exe), LAYA_HF_HOME and WHISPER_HF_HOME (model caches;
# unset: the default Hugging Face cache).
$root = Split-Path -Parent $PSScriptRoot
New-Item -ItemType Directory -Force "$root\logs" | Out-Null

function Listening($port) {
    [bool](Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
}

# 1. Laya on 127.0.0.1:8090 (weights already downloaded, offline).
if (-not (Listening 8090)) {
    if (-not $env:LAYA_SERVE) { Write-Host 'laya: set LAYA_SERVE to laya-serve.exe'; exit 1 }
    $env:LAYA_HOST = '127.0.0.1'; $env:LAYA_PORT = '8090'; $env:LAYA_MODELS = 'multilingual'
    $env:LAYA_DEVICE = 'cuda'; $env:HF_HUB_OFFLINE = '1'; $env:LAYA_LOG_LEVEL = 'warning'
    if ($env:LAYA_HF_HOME) { $env:HF_HOME = $env:LAYA_HF_HOME }
    Start-Process -FilePath $env:LAYA_SERVE -WindowStyle Hidden `
        -RedirectStandardOutput "$root\logs\laya.out.log" -RedirectStandardError "$root\logs\laya.err.log"
    Write-Host 'laya: starting...'
    for ($i = 0; $i -lt 60 -and -not (Listening 8090); $i++) { Start-Sleep -Seconds 2 }
}
Write-Host ('laya: ' + $(if (Listening 8090) { 'up' } else { 'NOT UP - see logs\laya.err.log' }))

# 2. Voice service: Py4J callback on 25334, /say on 8091.
if (-not (Listening 8091)) {
    $env:PYTHONIOENCODING = 'utf-8'; $env:HF_HUB_OFFLINE = '1'
    if ($env:WHISPER_HF_HOME) { $env:HF_HOME = $env:WHISPER_HF_HOME } else { Remove-Item Env:HF_HOME -ErrorAction SilentlyContinue }
    Start-Process -FilePath "$root\.venv-voice\Scripts\python.exe" -ArgumentList '-u', 'voice\service.py' `
        -WorkingDirectory $root -WindowStyle Hidden `
        -RedirectStandardOutput "$root\logs\voice.out.log" -RedirectStandardError "$root\logs\voice.err.log"
    Write-Host 'voice: starting (Whisper loads ~10 s)...'
    for ($i = 0; $i -lt 45 -and -not (Listening 8091); $i++) { Start-Sleep -Seconds 2 }
}
Write-Host ('voice: ' + $(if (Listening 8091) { 'up' } else { 'NOT UP - see logs\voice.err.log' }))

# 3. The agent (settings in scripts\run-agent.sh, secrets in .env).
if (-not (Get-Process pyhtun -ErrorAction SilentlyContinue)) {
    Start-Process -FilePath 'C:\Program Files\Git\bin\bash.exe' -ArgumentList "$root/scripts/run-agent.sh" -WindowStyle Hidden
    Start-Sleep -Seconds 3
}
Write-Host ('agent: ' + $(if (Get-Process pyhtun -ErrorAction SilentlyContinue) { 'running - log: logs\pyhtun.log' } else { 'NOT RUNNING' }))

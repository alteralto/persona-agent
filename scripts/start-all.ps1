# Start everything the character needs: Laya (decisions), the voice service (Whisper + Piper +
# Py4J to the mod) and the agent. Start Minecraft and enter a world first.
#   powershell -ExecutionPolicy Bypass -File F:\persona-agent\scripts\start-all.ps1
$root = Split-Path -Parent $PSScriptRoot
New-Item -ItemType Directory -Force "$root\logs" | Out-Null

function Listening($port) {
    [bool](Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
}

# 1. Laya on 127.0.0.1:8090 (weights on F:, offline).
if (-not (Listening 8090)) {
    $env:LAYA_HOST = '127.0.0.1'; $env:LAYA_PORT = '8090'; $env:LAYA_MODELS = 'multilingual'
    $env:LAYA_DEVICE = 'cuda'; $env:HF_HOME = 'F:\laya\hf'; $env:HF_HUB_OFFLINE = '1'; $env:LAYA_LOG_LEVEL = 'warning'
    Start-Process -FilePath 'F:\laya\.venv-gpu\Scripts\laya-serve.exe' -WindowStyle Hidden `
        -RedirectStandardOutput "$root\logs\laya.out.log" -RedirectStandardError "$root\logs\laya.err.log"
    Write-Host 'laya: starting...'
    for ($i = 0; $i -lt 60 -and -not (Listening 8090); $i++) { Start-Sleep -Seconds 2 }
}
Write-Host ('laya: ' + $(if (Listening 8090) { 'up' } else { 'NOT UP - see logs\laya.err.log' }))

# 2. Voice service: Py4J callback on 25334, /say on 8091.
if (-not (Listening 8091)) {
    $env:PYTHONIOENCODING = 'utf-8'; $env:HF_HUB_OFFLINE = '1'
    $env:HF_HOME = 'F:\WinPython64-3.13.11.0dot_post1\whisper_models\hf_cache'
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

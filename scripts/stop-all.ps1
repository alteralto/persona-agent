# Stop the agent, the voice service and Laya. Safe to run any time; Minecraft is not touched.
#   powershell -ExecutionPolicy Bypass -File scripts\stop-all.ps1
Get-Process pyhtun -ErrorAction SilentlyContinue | Stop-Process -Force
Get-CimInstance Win32_Process |
    Where-Object { $_.CommandLine -match 'voice\\service\.py|voice/service\.py|laya-serve' } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep -Milliseconds 500
$left = @(Get-Process pyhtun -ErrorAction SilentlyContinue).Count +
        @(Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -match 'service\.py|laya-serve' -and $_.Name -ne 'powershell.exe' }).Count
Write-Host $(if ($left -eq 0) { 'stopped: agent, voice, laya' } else { "still running: $left process(es)" })

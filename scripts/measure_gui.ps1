# measure_gui.ps1 — GUI 进程内存/CPU 采样（真实证据脚本）
# 用法：powershell -NoProfile -File scripts/measure_gui.ps1 -ProcName spike1 -Seconds 8 -IntervalMs 1200 -OutFile samples.txt
param(
    [Parameter(Mandatory=$true)][string]$ProcName,
    [int]$Seconds = 8,
    [int]$IntervalMs = 1200,
    [string]$OutFile = ""
)

$samples = @()
$deadline = (Get-Date).AddSeconds($Seconds)
$i = 0
while ((Get-Date) -lt $deadline) {
    $p = Get-Process -Name $ProcName -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $p) { break }
    $i++
    $samples += [PSCustomObject]@{
        Sample   = $i
        Time     = (Get-Date).ToString("HH:mm:ss")
        WS_MB    = [math]::Round($p.WorkingSet64 / 1MB, 1)
        Private_MB = [math]::Round($p.PrivateMemorySize64 / 1MB, 1)
        CPUMs    = [math]::Round($p.TotalProcessorTime.TotalMilliseconds, 0)
        Threads  = $p.Threads.Count
        Handles  = $p.HandleCount
    }
    Start-Sleep -Milliseconds $IntervalMs
}

# 派生：相邻样本的 CPU 增量（%/core，按采样间隔归一）
for ($j = 1; $j -lt $samples.Count; $j++) {
    $dt = $IntervalMs
    $samples[$j] | Add-Member -NotePropertyName CPUPerCore -NotePropertyValue ([math]::Round(($samples[$j].CPUMs - $samples[$j-1].CPUMs) / $dt * 100, 2))
}

if ($samples.Count -gt 0) {
    $first = $samples[0]
    $summary = [PSCustomObject]@{
        ProcName      = $ProcName
        Samples       = $samples.Count
        WS_MinMB      = ($samples | Measure-Object WS_MB -Minimum).Minimum
        WS_MaxMB      = ($samples | Measure-Object WS_MB -Maximum).Maximum
        Private_MinMB = ($samples | Measure-Object Private_MB -Minimum).Minimum
        Private_MaxMB = ($samples | Measure-Object Private_MB -Maximum).Maximum
        IdleCPUPerCore_Max = ($samples | Select-Object -Skip 1 | Measure-Object CPUPerCore -Maximum).Maximum
    }
    $result = [PSCustomObject]@{ Summary = $summary; Samples = $samples }
    $text = $result | ConvertTo-Json -Depth 4
    if ($OutFile -ne "") { $text | Out-File -Encoding utf8 $OutFile }
    Write-Output $text
} else {
    Write-Output ('{"error": "process not found or exited before sampling"}')
}

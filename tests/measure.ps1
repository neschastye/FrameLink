param(
    [Parameter(Mandatory=$true)][int]$ServerId,
    [Parameter(Mandatory=$true)][int]$DashboardId,
    [Parameter(Mandatory=$true)][int]$ObsId,
    [ValidateRange(2,3600)][int]$Seconds=15
)
$ErrorActionPreference='Stop'
function Get-TreeSample([int]$RootId,$Rows,[bool]$IncludeChildren) {
    $ids = [Collections.Generic.HashSet[int]]::new()
    [void]$ids.Add($RootId)
    do {
        $count = $ids.Count
        foreach ($row in $Rows) {
            if (!$IncludeChildren) { break }
            if ($ids.Contains([int]$row.ParentProcessId)) { [void]$ids.Add([int]$row.ProcessId) }
        }
    } while ($ids.Count -gt $count)
    $times = @{}
    $ws = [int64]0
    foreach ($id in $ids) {
        $proc = Get-Process -Id $id -ErrorAction SilentlyContinue
        if ($null -ne $proc) {
            $times["$id/$($proc.StartTime.Ticks)"] = $proc.TotalProcessorTime.TotalSeconds
            $ws += $proc.WorkingSet64
        }
    }
    [pscustomobject]@{Times=$times;WorkingSet=$ws}
}
$roots=[ordered]@{Server=$ServerId;Dashboard=$DashboardId;OBS=$ObsId}
$before=@{}
$rows=Get-CimInstance Win32_Process -Property ProcessId,ParentProcessId
foreach ($key in $roots.Keys){$before[$key]=Get-TreeSample $roots[$key] $rows ($key -ne 'Server')}
$timer=[Diagnostics.Stopwatch]::StartNew()
Start-Sleep -Seconds $Seconds
$rows=Get-CimInstance Win32_Process -Property ProcessId,ParentProcessId
foreach ($key in $roots.Keys){
    $after=Get-TreeSample $roots[$key] $rows ($key -ne 'Server')
    $delta=0
    foreach ($k in $after.Times.Keys){if ($before[$key].Times.ContainsKey($k)){$delta += $after.Times[$k]-$before[$key].Times[$k]}}
    [pscustomobject]@{Component=$key; Seconds=[math]::Round($timer.Elapsed.TotalSeconds,1);WorkingSetMiB=[math]::Round($after.WorkingSet/1MB,1);CPUPercentMachine=[math]::Round(100*$delta/$timer.Elapsed.TotalSeconds/[Environment]::ProcessorCount,2)}
}

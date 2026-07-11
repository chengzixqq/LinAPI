# LinAPI 本地开发一键启动（仅供开发）。
#
# 做三件事：
#   1. 后台起 cmd/devredis（进程内 miniredis，暴露 127.0.0.1:6379）
#   2. 等端口就绪
#   3. 前台起网关（-config config.dev.yaml，admin.enabled=true 挂载 /console）
#
# 前端联调：另开一个终端 `cd web; npm run dev`（:5173，proxy 到 :8080），
# 或先 `cd web; npm run build` 让产物 embed 进二进制，直接访问 http://localhost:8080/console
#
# Ctrl+C 停止网关；脚本会顺带清理后台的 devredis。

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot

Write-Host "[dev] 启动进程内 miniredis (cmd/devredis)..." -ForegroundColor Cyan
$redis = Start-Process -FilePath "go" -ArgumentList "run", "./cmd/devredis" `
    -WorkingDirectory $root -PassThru -NoNewWindow

# 等 miniredis 端口就绪（最多 ~30s，覆盖首次 go build 编译时间）。
$ready = $false
for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Milliseconds 500
    try {
        $c = New-Object System.Net.Sockets.TcpClient
        $c.Connect("127.0.0.1", 6379)
        $c.Close()
        $ready = $true
        break
    } catch { }
}
if (-not $ready) {
    Write-Host "[dev] miniredis 未能在超时内就绪，放弃启动" -ForegroundColor Red
    if ($redis -and -not $redis.HasExited) { Stop-Process -Id $redis.Id -Force }
    exit 1
}
Write-Host "[dev] miniredis 就绪 (127.0.0.1:6379)" -ForegroundColor Green

try {
    Write-Host "[dev] 启动网关 (config.dev.yaml, /console 已挂载)..." -ForegroundColor Cyan
    Write-Host "[dev] 控制台: http://localhost:8080/console  (admin / admin12345)" -ForegroundColor Yellow
    & go run ./cmd/linapi -config config.dev.yaml
} finally {
    Write-Host "`n[dev] 清理后台 miniredis..." -ForegroundColor Cyan
    if ($redis -and -not $redis.HasExited) { Stop-Process -Id $redis.Id -Force }
}

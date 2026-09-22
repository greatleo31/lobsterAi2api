@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"
title LobsterAI 2API 服务

rem ===== 上游配置（要改就改这两行）=====
if "%LB2A_UPSTREAM_BASE%"=="" set "LB2A_UPSTREAM_BASE=https://lobsterai-server.youdao.com"
if "%LB2A_LOGIN_PORTAL%"=="" set "LB2A_LOGIN_PORTAL=https://lobsterai.youdao.com"

if not exist "config.json" (
  if exist "config.example.json" (
    copy /y "config.example.json" "config.json" >nul
    echo [准备] 已从 config.example.json 生成 config.json
  )
)

if not exist "auths" mkdir "auths"

rem 每次启动都重新编译：源码改了但 exe 是旧的，这里会把它换掉。
rem Go 有构建缓存，没改动时几乎秒过。
echo [准备] 编译 lobsterai2api.exe ...
go build -o lobsterai2api.exe ./cmd/server
if errorlevel 1 (
  if exist "lobsterai2api.exe" (
    echo [警告] 编译失败，沿用已有的 lobsterai2api.exe 继续启动。
    echo         旧服务可能仍占用该文件，先关掉旧窗口再重跑本脚本。
  ) else (
    goto :err
  )
)

echo ============================================================
echo   LobsterAI 2API 服务
echo ============================================================
echo   监听地址 : http://127.0.0.1:8367
echo   上游 API : %LB2A_UPSTREAM_BASE%
echo   账号目录 : %~dp0auths
echo   配置     : %~dp0config.json
echo.
echo   任务状态 : curl -s http://127.0.0.1:8367/status
echo   关闭本窗口 = 停止服务
echo ============================================================
echo.

lobsterai2api.exe -config config.json
echo.
echo 服务已退出（端口被占用？配置有误？看上面的日志）。
pause
exit /b 0

:err
echo.
echo 编译失败，请检查 Go 环境和源码。
pause
exit /b 1

@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion
cd /d "%~dp0"
title LobsterAI 登录

rem ===== 上游配置（要改就改这两行，或先 set 好环境变量）=====
if "%LB2A_UPSTREAM_BASE%"=="" set "LB2A_UPSTREAM_BASE=https://lobsterai-server.youdao.com"
if "%LB2A_LOGIN_PORTAL%"=="" set "LB2A_LOGIN_PORTAL=https://lobsterai.youdao.com"

echo ============================================================
echo   LobsterAI 登录
echo ============================================================
echo   上游 API : %LB2A_UPSTREAM_BASE%
echo   登录门户 : %LB2A_LOGIN_PORTAL%
echo.

if not exist "auths" mkdir "auths"
rem login.exe 的状态文件写在 “当前盘符:\tmp”，先把目录备好
if not exist "%~d0\tmp" mkdir "%~d0\tmp"

if not exist "login.exe" (
  echo [1/3] 编译 login.exe ...
  go build -o login.exe ./cmd/login
  if errorlevel 1 goto :err
) else (
  echo [1/3] 使用已编译的 login.exe
)

set "URLFILE=lb2a-url.tmp"
set "ERRFILE=lb2a-err.tmp"
del "%URLFILE%" "%ERRFILE%" >nul 2>nul

echo [2/3] 启动本地回调服务器 ...
start "lb2a-callback" /min cmd /c "login.exe url > %URLFILE% 2> %ERRFILE%"

set "LOGIN_URL="
set /a WAIT=0
:wait_url
if exist "%URLFILE%" set /p LOGIN_URL=<"%URLFILE%"
if defined LOGIN_URL goto :got_url
set /a WAIT+=1
if %WAIT% GEQ 30 (
  echo.
  echo [错误] 30 秒没拿到登录链接，报错信息：
  type "%ERRFILE%" 2>nul
  goto :err
)
<nul set /p "=."
ping -n 2 127.0.0.1 >nul 2>nul
goto :wait_url

:got_url
echo.
echo.
echo [3/3] 正在打开浏览器 ...
start "" "!LOGIN_URL!"
echo.
echo   如果浏览器没自动弹出，把下面这条链接复制到浏览器打开：
echo.
echo   !LOGIN_URL!
echo.
echo   用微信扫码完成登录；登录成功后本窗口会自动继续，不用手动按键。
echo.
login.exe poll
if errorlevel 1 goto :err

set /a TRY=0
:cleanup
del "%URLFILE%" "%ERRFILE%" >nul 2>nul
if not exist "%URLFILE%" goto :cleaned
set /a TRY+=1
if %TRY% GEQ 10 goto :cleaned
ping -n 2 127.0.0.1 >nul 2>nul
goto :cleanup
:cleaned
echo.
echo ==== 已保存的账号文件 ====
dir /b "auths\lobsterai-*.json"
echo.
echo 登录完成。重启服务即可加载新账号（当前窗口可关闭）：
echo     lobsterai2api.exe -config config.json
echo.
pause
exit /b 0

:err
echo.
echo 登录失败，请把上面的信息发出来排查。
echo.
pause
exit /b 1

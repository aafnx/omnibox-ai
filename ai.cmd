@echo off
pwsh.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0ai.ps1" %*
exit /b %errorlevel%

param(
    [switch]$Watch
)

$ErrorActionPreference = "Stop"

# Tailwind CSS Standalone CLI Builder for Windows (Zero Node.js / Zero npm)
$rootDir = (Resolve-Path "$PSScriptRoot\..").Path
$toolsDir = Join-Path $rootDir "tools"
$tailwindExe = Join-Path $toolsDir "tailwindcss.exe"

if (-not (Test-Path $tailwindExe)) {
    Write-Host "[+] Lade offizielle Tailwind CSS Standalone CLI für Windows herunter..."
    New-Item -ItemType Directory -Force -Path $toolsDir | Out-Null
    Invoke-WebRequest -Uri "https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-windows-x64.exe" -OutFile $tailwindExe
}

$inputCss = Join-Path $rootDir "backend\internal\web\static\input.css"
$outputCss = Join-Path $rootDir "backend\internal\web\static\tailwind.css"
$staticTarget = Join-Path $rootDir "backend\static\tailwind.css"
$inputTarget = Join-Path $rootDir "backend\static\input.css"

if ($Watch) {
    Write-Host "[+] Starte Tailwind CSS Watcher..."
    & $tailwindExe -i $inputCss -o $outputCss --minify --watch
} else {
    Write-Host "[+] Kompiliere Tailwind CSS..."
    & $tailwindExe -i $inputCss -o $outputCss --minify
    Copy-Item $outputCss $staticTarget -Force
    Copy-Item $inputCss $inputTarget -Force
    Write-Host "[OK] Tailwind CSS erfolgreich kompiliert und synchronisiert!"
}


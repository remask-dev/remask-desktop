param(
  [Parameter(Mandatory = $true)]
  [string]$InstallerPath,
  [string]$ModelID = "openai-privacy-filter-q4f16"
)

$ErrorActionPreference = "Stop"
$installer = (Resolve-Path $InstallerPath).Path
$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("remask-windows-smoke-" + [guid]::NewGuid().ToString("N"))
$installDir = Join-Path $testRoot "app"
$dataDir = Join-Path $testRoot "data"
$coreLog = Join-Path $testRoot "core.log"

New-Item -ItemType Directory -Force -Path $installDir, $dataDir | Out-Null

try {
  $install = Start-Process -FilePath $installer -ArgumentList @("/S", "/D=$installDir") -Wait -PassThru
  if ($install.ExitCode -ne 0) {
    throw "installer exited with code $($install.ExitCode)"
  }

  $desktop = Join-Path $installDir "remask-desktop.exe"
  $core = Join-Path $installDir "remask-core.exe"
  $runtime = Join-Path $installDir "resources\onnxruntime\onnxruntime.dll"
  $models = Join-Path $installDir "resources\models"
  $modelManifest = Join-Path $models "$ModelID\manifest.json"
  $managedModels = Join-Path $dataDir "models"
  $required = @(
    $desktop,
    $core,
    $runtime,
    $modelManifest,
    (Join-Path $installDir "msvcp140.dll"),
    (Join-Path $installDir "msvcp140_1.dll"),
    (Join-Path $installDir "vcruntime140.dll"),
    (Join-Path $installDir "vcruntime140_1.dll")
  )
  foreach ($file in $required) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) {
      throw "installed runtime file is missing: $file"
    }
  }

  $coreArguments = @(
    "-self-test",
    "-models-dir", $managedModels,
    "-builtin-models-dir", $models,
    "-active-model", $ModelID,
    "-onnxruntime-lib", $runtime,
    "-onnx-provider", "cpu",
    "-data-dir", $dataDir
  )
  $coreTest = Start-Process -FilePath $core -ArgumentList $coreArguments -Wait -PassThru -NoNewWindow -RedirectStandardError $coreLog
  if ($coreTest.ExitCode -ne 0) {
    $details = if (Test-Path $coreLog) { Get-Content $coreLog -Raw } else { "no core log" }
    throw "remask-core self-test failed with code $($coreTest.ExitCode):`n$details"
  }

  $desktopTest = Start-Process -FilePath $desktop -PassThru
  Start-Sleep -Seconds 8
  if ($desktopTest.HasExited) {
    throw "remask-desktop exited during the startup smoke test with code $($desktopTest.ExitCode)"
  }
  Stop-Process -Id $desktopTest.Id -Force
  Write-Host "Windows installer smoke test passed: runtime, model, and desktop startup"
}
finally {
  $uninstaller = Join-Path $installDir "uninstall.exe"
  if (Test-Path -LiteralPath $uninstaller -PathType Leaf) {
    Start-Process -FilePath $uninstaller -ArgumentList "/S" -Wait | Out-Null
  }
  if (Test-Path -LiteralPath $testRoot) {
    Remove-Item -LiteralPath $testRoot -Recurse -Force
  }
}

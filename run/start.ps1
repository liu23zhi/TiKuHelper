$ErrorActionPreference = 'Stop'
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$composeFile = Join-Path $scriptDir 'docker-compose.yml'
$qrFile = Join-Path $scriptDir 'Wechat_qc_code.jpg'
$overrideFile = $null
$exitCode = 1

try {
    $composeInvocation = @('compose', '--file', $composeFile)

    if (Test-Path -LiteralPath $qrFile -PathType Leaf) {
        $overrideFile = Join-Path ([System.IO.Path]::GetTempPath()) ("tikuhelper-compose-override-{0}.yml" -f [guid]::NewGuid().ToString('N'))
        $yamlSource = $qrFile.Replace("'", "''")
        $overrideContents = @"
services:
  tikuhelper:
    volumes:
      - type: bind
        source: '$yamlSource'
        target: /usr/share/nginx/html/Wechat_qc_code.jpg
        read_only: true
"@
        [System.IO.File]::WriteAllText($overrideFile, $overrideContents, [System.Text.UTF8Encoding]::new($false))
        $composeInvocation += @('--file', $overrideFile)
    }

    $composeInvocation += @($args)
    & docker @composeInvocation
    $exitCode = $LASTEXITCODE
}
catch {
    $exitCode = 1
    [Console]::Error.WriteLine($_.Exception.Message)
}
finally {
    if ($null -ne $overrideFile -and (Test-Path -LiteralPath $overrideFile -PathType Leaf)) {
        Remove-Item -LiteralPath $overrideFile -Force -ErrorAction SilentlyContinue
    }
}

exit $exitCode

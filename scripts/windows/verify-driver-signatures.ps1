param(
    [string]$PackageZip = "$PSScriptRoot\..\..\internal\windrivers\packages.zip",
    [string]$Manifest = "$PSScriptRoot\..\..\internal\windrivers\packages.sha256.json",
    [string]$OutputPath
)
$ErrorActionPreference = 'Stop'
$dir = Join-Path ([IO.Path]::GetTempPath()) ("urdp-signatures-" + [guid]::NewGuid())
try {
    Expand-Archive -Path $PackageZip -DestinationPath $dir
    $hashes = Get-Content $Manifest -Raw | ConvertFrom-Json
    $report = @($hashes.PSObject.Properties | ForEach-Object {
        $file = Join-Path $dir $_.Name
        $hash = (Get-FileHash $file -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($hash -ne $_.Value) { throw "SHA256 mismatch: $($_.Name)" }
        $status = $null
        $subject = $null
        if ([IO.Path]::GetExtension($file) -in @('.cat', '.sys')) {
            $sig = Get-AuthenticodeSignature $file
            $status = $sig.Status.ToString()
            $subject = $sig.SignerCertificate.Subject
            if ($status -ne 'Valid' -or $subject -notlike 'CN=Microsoft Windows Hardware Compatibility Publisher,*') {
                throw "Invalid Microsoft driver signature: $($_.Name): $status $subject"
            }
        }
        [pscustomobject]@{ File=$_.Name; SHA256=$hash; Status=$status; Signer=$subject }
    })
    $json = ($report | ConvertTo-Json) -replace "`r`n", "`n"
    if ($OutputPath) { [IO.File]::WriteAllText($OutputPath, $json + "`n") }
    $json
} finally {
    Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
}

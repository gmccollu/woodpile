<#
.SYNOPSIS
  Optional Authenticode signing for woodpile.exe and the MSI.

.DESCRIPTION
  Signs the given files with one provider, then checks each signature. With
  -Provider none (no secrets configured) it prints a note and does nothing.

  azure     Azure Trusted Signing, through the TrustedSigning PowerShell module.
            Auth: AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET (an app registration
            with the "Trusted Signing Certificate Profile Signer" role).
            Account: AZURE_SIGNING_ENDPOINT (e.g. https://eus.codesigning.azure.net/),
            AZURE_SIGNING_ACCOUNT, AZURE_SIGNING_PROFILE.

  signpath  SignPath, through the SignPath PowerShell module.
            SIGNPATH_API_TOKEN, SIGNPATH_ORGANIZATION_ID, SIGNPATH_PROJECT_SLUG,
            SIGNPATH_SIGNING_POLICY_SLUG. Note: SignPath's free OSS program expects the
            build origin to be verified through its own GitHub integration; check their
            current requirements when signing up.

  UNTESTED: neither provider has been run against a real account yet.

.EXAMPLE
  ./packaging/windows/sign.ps1 -Provider azure -Files dist/windows/amd64/woodpile.exe
#>
[CmdletBinding()]
param(
    [ValidateSet("none", "azure", "signpath")]
    [string]$Provider = "none",
    [Parameter(Mandatory = $true)]
    [string[]]$Files
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if ($Provider -eq "none") {
    Write-Host "No signing provider configured; leaving $($Files -join ', ') unsigned."
    exit 0
}

$paths = $Files | ForEach-Object { (Resolve-Path $_).Path }

function Require-Env([string[]]$Names) {
    foreach ($n in $Names) {
        if (-not [Environment]::GetEnvironmentVariable($n)) { throw "$n is not set" }
    }
}

switch ($Provider) {
    "azure" {
        Require-Env @("AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET",
            "AZURE_SIGNING_ENDPOINT", "AZURE_SIGNING_ACCOUNT", "AZURE_SIGNING_PROFILE")
        Install-Module -Name TrustedSigning -Scope CurrentUser -Force -Repository PSGallery
        Invoke-TrustedSigning `
            -Endpoint $env:AZURE_SIGNING_ENDPOINT `
            -CodeSigningAccountName $env:AZURE_SIGNING_ACCOUNT `
            -CertificateProfileName $env:AZURE_SIGNING_PROFILE `
            -Files ($paths -join ",") `
            -FileDigest SHA256 `
            -TimestampRfc3161 "http://timestamp.acs.microsoft.com" `
            -TimestampDigest SHA256
    }
    "signpath" {
        Require-Env @("SIGNPATH_API_TOKEN", "SIGNPATH_ORGANIZATION_ID", "SIGNPATH_PROJECT_SLUG",
            "SIGNPATH_SIGNING_POLICY_SLUG")
        Install-Module -Name SignPath -Scope CurrentUser -Force -Repository PSGallery
        foreach ($p in $paths) {
            Submit-SigningRequest `
                -ApiToken $env:SIGNPATH_API_TOKEN `
                -OrganizationId $env:SIGNPATH_ORGANIZATION_ID `
                -ProjectSlug $env:SIGNPATH_PROJECT_SLUG `
                -SigningPolicySlug $env:SIGNPATH_SIGNING_POLICY_SLUG `
                -InputArtifactPath $p `
                -OutputArtifactPath $p `
                -WaitForCompletion `
                -Force
        }
    }
}

foreach ($p in $paths) {
    $sig = Get-AuthenticodeSignature -FilePath $p
    if ($sig.Status -ne "Valid") { throw "Signature on $p is $($sig.Status): $($sig.StatusMessage)" }
    Write-Host "Signed: $p ($($sig.SignerCertificate.Subject))"
}

$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent $PSScriptRoot
$OutDir = Join-Path $Root "resources\windows"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) "mgnote-win-pdf-build"
New-Item -ItemType Directory -Force -Path $TmpDir | Out-Null

@"
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net8.0-windows10.0.19041.0</TargetFramework>
    <AssemblyName>mgnote-pdf</AssemblyName>
    <PublishSingleFile>true</PublishSingleFile>
    <SelfContained>false</SelfContained>
    <ImplicitUsings>enable</ImplicitUsings>
    <Nullable>enable</Nullable>
  </PropertyGroup>
  <ItemGroup>
    <Compile Include="$(Join-Path $PSScriptRoot 'render-pdf-win.cs')" />
  </ItemGroup>
</Project>
"@ | Set-Content -Path (Join-Path $TmpDir "mgnote-pdf.csproj") -Encoding UTF8

dotnet publish (Join-Path $TmpDir "mgnote-pdf.csproj") -c Release -r win-x64 -o $OutDir
Write-Host "Built $OutDir\mgnote-pdf.exe"

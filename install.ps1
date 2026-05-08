$os = "windows"
$arch = if ($IsARM64) { "arm64" } else { "amd64" }

Write-Host "Detecting latest version..." -ForegroundColor Cyan
$release = Invoke-RestMethod -Uri "https://api.github.com/repos/Facets-cloud/flow/releases/latest"
$version = $release.tag_name.Substring(1)

$filename = "flow_${version}_${os}_${arch}.tar.gz"
$url = "https://github.com/Facets-cloud/flow/releases/latest/download/$filename"

Write-Host "Downloading $filename..." -ForegroundColor Cyan
Invoke-WebRequest -Uri $url -OutFile "flow.tar.gz"

Write-Host "Extracting..." -ForegroundColor Cyan
tar -xzf flow.tar.gz

$dest = "$HOME\AppData\Local\Microsoft\WindowsApps"
Write-Host "Installing to $dest..." -ForegroundColor Cyan
Move-Item -Path "flow.exe" -Destination "$dest\flow.exe" -Force

# Clean up
Remove-Item "flow.tar.gz"

Write-Host "Done! To complete the setup, run:" -ForegroundColor Green
Write-Host "  flow init" -ForegroundColor Green

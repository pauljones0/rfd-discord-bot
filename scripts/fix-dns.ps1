# Run this as Administrator to fix DNS for cloudflared
# Your router DNS can't resolve api.trycloudflare.com — this adds a hosts entry.
Add-Content -Path "C:\Windows\System32\drivers\etc\hosts" -Value "`n104.16.230.132 api.trycloudflare.com"
Write-Host "Added hosts entry for api.trycloudflare.com" -ForegroundColor Green

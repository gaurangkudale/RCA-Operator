package dashboard

import "embed"

//go:embed static/index.html static/dashboard.css static/lucide-local.js static/images
var staticFS embed.FS

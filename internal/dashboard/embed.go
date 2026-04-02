package dashboard

import "embed"

//go:embed static/index.html static/images
var staticFS embed.FS

package web

import "embed"

//go:embed index.html app.js style.css
var Assets embed.FS

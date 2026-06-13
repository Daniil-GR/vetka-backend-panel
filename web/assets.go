package web

import "embed"

//go:embed templates/*.html templates/partials/*.html static/*.css static/*.js static/vendor/*
var FS embed.FS

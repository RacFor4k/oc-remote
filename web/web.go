package web

import "embed"

//go:embed *.html *.css *.js
var FS embed.FS

func Read(name string) ([]byte, error) {
	return FS.ReadFile(name)
}

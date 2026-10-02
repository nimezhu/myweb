// Package manager embeds myweb's web file manager: a single page that browses,
// previews, uploads, renames and deletes files, shows versions and the trash,
// and lists plugin commands, all through the server's own API.
package manager

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:web
var files embed.FS

// FS serves the manager's files (index.html at the root).
func FS() http.FileSystem {
	sub, err := fs.Sub(files, "web")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}

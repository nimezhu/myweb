package myserver

import (
	"html/template"
	"net/http"
	"strings"
)

/* TODO Template for App Portal */

type MyApp struct {
	Title   string
	Version string
	URL     string
}

var tmpl = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1, user-scalable=0">
    <link rel="stylesheet" href="{{.URL}}/style.css">
    <title>{{if .Title}}{{.Title}}{{if .Version}} @{{.Version}}{{end}}{{else}}YouData App{{end}}</title>
</head>
<body>
    <script src="{{.URL}}/main.js" type="module"></script>
</body>
</html>
`))

// appHandler serves /io/<app> as a shell page loading <appServer>/<app>/main.js.
func appHandler(appServer string) http.Handler {
	troot := strings.TrimRight(appServer, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app := MyApp{URL: troot + "/" + strings.Trim(r.URL.Path, "/")}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, app); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

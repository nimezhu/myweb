package myserver

import (
	"encoding/json"
	"github.com/otiai10/opengraph/v2"
	"net/http"
)

type Info struct {
	Success int                  `json:"success"`
	Meta    *opengraph.OpenGraph `json:"meta"`
}

func FetchURLHandleFunc(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	ogp, err := opengraph.Fetch(url)
	if err == nil {
		j, err2 := json.Marshal(Info{1, ogp})
		if err2 == nil {
			w.Write(j)
		} else {
			w.Write([]byte("{success:0, error:1}"))
		}
	} else {
		w.Write([]byte("{success:0, error:2}"))
	}
}

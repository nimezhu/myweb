package myserver

import (
	"net/http"
	"net/url"
	"time"

	"github.com/otiai10/opengraph/v2"
)

type Info struct {
	Success int                  `json:"success"`
	Meta    *opengraph.OpenGraph `json:"meta"`
}

var fetchClient = &http.Client{Timeout: 15 * time.Second}

func FetchURLHandleFunc(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}
	ogp, err := opengraph.Fetch(raw, opengraph.Intent{
		Context:    r.Context(),
		HTTPClient: fetchClient,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "fetch failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, Info{1, ogp})
}

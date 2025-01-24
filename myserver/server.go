package myserver

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"syscall"

	"github.com/rs/cors"
)

var VERSION = "0.0.1"

type IServer interface {
	Shutdown(ctx context.Context) error
}

//go:embed all:ctrl
var embededFiles embed.FS

func getFS() http.FileSystem {
	fsys, err := fs.Sub(embededFiles, "ctrl")
	if err != nil {
		panic(err)
	}
	return http.FS(fsys)
}

// func startServer(port int, secure bool, global bool, root string, web string, cookie bool) iServer {
func StartServer(root string, webFS http.FileSystem, port int, secure bool, global bool) IServer {
	CORS := []string{
		"https://codepen.io",
		"https://cdpn.io",
		"https://mycellome.com",
		"https://*.mycellome.com",
		"https://cellcycle.org",
		"https://*.cellcycle.org",
		"https://youdata.studio",
		"https://*.youdata.studio",
		"https://*.static.observableusercontent.com",
		"http://127.0.0.1:*",
		"https://127.0.0.1:*",
		"http://localhost:*",
		"https://localhost:*",
		//TODO: MOON SHOT SHOULD BE Parameters
	}
	corsOptions := cors.Options{
		AllowedOrigins:   CORS,
		AllowCredentials: true,
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization"},
	}

	//TODO Add Simpler Version ?
	mux0 := http.NewServeMux()
	mux0.Handle("/", http.FileServer(http.Dir(root)))

	mux1 := http.NewServeMux()
	mux1.HandleFunc("/", CreateLsHandleFunc(root))
	// mux := http.NewServeMux()

	mux2 := http.NewServeMux()
	mux2.HandleFunc("/", CreateWriteHandleFunc(root))

	mux3 := http.NewServeMux()
	mux3.HandleFunc("/", CreateRemoveHandleFunc(root))

	m := http.NewServeMux()
	webmux := http.NewServeMux()

	webmux.Handle("/", http.FileServer(webFS))

	cmux := http.NewServeMux()
	cmux.Handle("/", http.FileServer(getFS()))
	m.Handle("/ctrl/", http.StripPrefix("/ctrl", cmux))

	m.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(VERSION))
	})
	/*
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// TODO With Front Page...
			// w.Write([]byte("My Ome Server:" + VERSION))
		})
	*/
	m.HandleFunc("/cmd/fetchUrl", FetchURLHandleFunc)
	m.HandleFunc("/cmd/uploadFile", CreateUploadFileHandleFunc(root))
	m.HandleFunc("/cmd/stopServer", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			syscall.Kill(syscall.Getpid(), syscall.SIGINT)
		case http.MethodGet:
			w.Write([]byte("Post data only"))
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	m.Handle("/f/", http.StripPrefix("/f", mux0))
	m.Handle("/ls/", http.StripPrefix("/ls", mux1))
	m.Handle("/w/", http.StripPrefix("/w", mux2))
	m.Handle("/rm/", http.StripPrefix("/rm", mux3))
	m.Handle("/", webmux)

	handler := cors.New(corsOptions).Handler(m)
	var ipstr string
	if global {
		ipstr = ":" + strconv.Itoa(port)
	} else {
		ipstr = "127.0.0.1:" + strconv.Itoa(port)
	}
	log.Println("Root Directory", root)
	if secure {
		embeddedTLSserver := &EmbeddedServer{
			Certificate: c,
			Key:         k,
		}
		go func() {
			log.Println("Start a https server at " + ipstr)
			err := embeddedTLSserver.ListenAndServeTLS(ipstr, handler)
			if err != nil {
			}
		}()
		return embeddedTLSserver
	} else {
		server := &http.Server{Addr: ipstr, Handler: handler}
		go func() {
			log.Println("Start a http server at " + ipstr)
			if err := server.ListenAndServe(); err != nil {
			}
		}()
		return server
	}
}

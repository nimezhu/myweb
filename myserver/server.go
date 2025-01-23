package myserver

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"github.com/rs/cors"
)

var VERSION = "0.0.1"

type IServer interface {
	Shutdown(ctx context.Context) error
}

// func startServer(port int, secure bool, global bool, root string, web string, cookie bool) iServer {
func StartServer(root string, webFS http.FileSystem, port int, secure bool, global bool) IServer {
	CORS := []string{
		"https://codepen.io",
		"https://cdpn.io",
		"https://app.mycellome.com",
		"https://mycellome.com",
		"https://app.cellcycle.org",
		"https://han.cellcycle.org",
		"https://cellcycle.org",
		"https://youdata.studio",
		"https://app.youdata.studio",
		"https://*.static.observableusercontent.com",
		"http://127.0.0.1:*",
		"https://127.0.0.1:*",
		"http://localhost:*",
		"https://localhost:*",
		"https://demo.mycellome.com",
		//TODO: MOON SHOT SHOULD BE Parameters
		"http://visportaldev.iotnmoonshot.org.s3-website-us-east-1.amazonaws.com/",
		"https://visportaldev.iotnmoonshot.org.s3-website-us-east-1.amazonaws.com/",
		"https://cellome.biomisc.org",
		"https://*.iotnmoonshot.org",
		"http://*.iotnmoonshot.org",
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
	/*
		if len(web) > 0 {
			webmux := http.NewServeMux()
			//webmux.Handle("/", http.FileServer(http.Dir(web)))
			webmux.Handle("/", http.FileServer(http.Dir(web)))
			m.Handle("/web/", http.StripPrefix("/web", webmux))
		} else {
			webmux := http.NewServeMux()
			webmux.Handle("/", http.FileServer(getFileSystem()))
			m.Handle("/web/", http.StripPrefix("/web", webmux))
		}
	*/
	webmux.Handle("/", http.FileServer(webFS))
	// m.Handle("/web/", http.StripPrefix("/web", webmux))

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

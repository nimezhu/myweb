package myserver

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	// "runtime"
	"strconv"
	"syscall"

	"github.com/rs/cors"
)

var VERSION = "0.0.1"

type IServer interface {
	Shutdown(ctx context.Context) error
}

func killProcess(pid int) error {
	/*
		if runtime.GOOS == "windows" {
			p, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return p.Signal(syscall.SIGTERM)
		} else {
			return syscall.Kill(pid, syscall.SIGTERM)
		}
	*/
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)

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

// core := make(map[string]interface{})
// TODO Settings Include CORS ...
// service interface
// name string
// handle ...
// core settings ...
// core services ...
// TODO Using Core ...
func addServiceWrite(root string, m *http.ServeMux) {
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/", CreateWriteHandleFunc(root))
	mux3 := http.NewServeMux()
	mux3.HandleFunc("/", CreateRemoveHandleFunc(root))
	m.Handle("/w/", http.StripPrefix("/w", mux2))
	m.Handle("/rm/", http.StripPrefix("/rm", mux3))
	m.HandleFunc("/cmd/uploadFile", CreateUploadFileHandleFunc(root))
}

func addServiceFile(root string, m *http.ServeMux) {
	mux0 := http.NewServeMux()
	mux0.Handle("/", http.FileServer(http.Dir(root)))

	mux1 := http.NewServeMux()
	mux1.HandleFunc("/", CreateLsHandleFunc(root))
	m.Handle("/f/", http.StripPrefix("/f", mux0))
	m.Handle("/ls/", http.StripPrefix("/ls", mux1))
}

/* last added */
func addServiceWeb(webFS http.FileSystem, m *http.ServeMux) {
	webmux := http.NewServeMux()
	webmux.Handle("/", http.FileServer(webFS))
	m.Handle("/", webmux)
}

// func StartServer(root string, webFS http.FileSystem, port int, secure bool, global bool, readOnly bool) IServer {
func StartServer(core map[string]interface{}) IServer {
	root, _ := core["root"].(string)
	webFS, _ := core["webfs"].(http.FileSystem)
	port, _ := core["port"].(int)
	secure, _ := core["secure"].(bool)
	global, _ := core["global"].(bool)
	readOnly, _ := core["readOnly"].(bool)
	m, _ := core["mux"].(*http.ServeMux)
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
	// TODO Add Simpler Version ?
	// TODO Setting In Core ...

	addServiceFile(root, m)
	if !readOnly {
		addServiceWrite(root, m)
	} else {
		log.Println("Read only mode.")
	}

	//webmux := http.NewServeMux()
	//webmux.Handle("/", http.FileServer(webFS))

	cmux := http.NewServeMux()
	cmux.Handle("/", http.FileServer(getFS()))
	m.Handle("/ctrl/", http.StripPrefix("/ctrl", cmux))

	m.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(VERSION))
	})

	m.HandleFunc("/cmd/fetchUrl", FetchURLHandleFunc)
	m.HandleFunc("/cmd/stopServer", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			killProcess(syscall.Getpid())
		case http.MethodGet:
			w.Write([]byte("Post data only"))
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	// TODO Add Command Services Plugin in POST ...
	// cmd name and services ...
	// m.HandleFunc("/cmd/"+cmd , func() )
	// add kernel Map {string}{Interface{}} ...

	/* last added */
	/* TODO add prefix */
	addServiceWeb(webFS, m)

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

package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"

	. "myweb/myserver"
)

//go:embed all:web
var embededFiles embed.FS

func getFileSystem() http.FileSystem {
	log.Print("using embed mode")
	fsys, err := fs.Sub(embededFiles, "web")
	if err != nil {
		panic(err)
	}
	return http.FS(fsys)
}
func open(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}
func generateURL(port int, ssl bool) string {
	var h string
	if ssl {
		h = "https://127.0.0.1"
	} else {
		h = "http://127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", h, port)
}
func main() {
	port := flag.Int("p", 8433, "port to serve on")
	secure := flag.Bool("s", false, "https/http")
	global := flag.Bool("global", false, "serve to public ip")
	directory := flag.String("d", ".", "the root directory of data files")
	web := flag.String("web", "", "use static web (default: embedded)")
	flag.Parse()
	var webFS http.FileSystem
	if len(*web) > 0 {
		webFS = http.Dir(*web)
	} else {
		webFS = getFileSystem()
	}
	server := StartServer(*directory, webFS, *port, *secure, *global)
	go open(generateURL(*port, *secure))
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	// TODO Add Cmd From Web Client ...
	// Waiting for SIGINT (kill -2)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Println("Server shutdown error")
	} else {
		log.Println("Server Shutdown")
	}

}

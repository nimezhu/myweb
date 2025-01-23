package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
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

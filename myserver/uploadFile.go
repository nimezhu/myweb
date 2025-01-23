package myserver

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
)

func CreateUploadFileHandleFunc(root string) func(w http.ResponseWriter, r *http.Request) {
	var MaxFileSize int64 = 10 * 1024 * 1024
	errMessage := errRet{
		0,
		"File Too Large (max 10Mb)",
	}
	em, _ := json.Marshal(errMessage)

	return func(w http.ResponseWriter, r *http.Request) {

		if r.ContentLength > MaxFileSize {
			if flusher, ok := w.(http.Flusher); ok {
				w.Header().Set("Connection", "close")
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(em)))
				w.WriteHeader(http.StatusExpectationFailed)
				w.Write(em)
				flusher.Flush()
			}
			//log.Println(r.ContentLength)
			log.Println("file too large")
			//r.Body.Close()
			// w.Write(em)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxFileSize)

		r.ParseMultipartForm(10 << 20)
		// FormFile returns the first file for the given key `myFile`
		// it also returns the FileHeader so we can get the Filename,
		// the Header and the size of the file
		file, handler, err := r.FormFile("myFile")
		if err != nil {
			log.Println("Error Retrieving the File")
			log.Println(err)
			return
		}
		defer file.Close()
		fileBytes, err := ioutil.ReadAll(file)
		if err != nil {
			log.Println(err)
		}
		b := md5.Sum(fileBytes)
		md5Sum := fmt.Sprintf("%x", b)
		id := md5Sum + "_" + handler.Filename //local file path
		p := root + "/uploads/" + id
		dir := "/uploads/" + id // relative path ..
		if err != nil {
			w.Write([]byte("{success:0}"))
			log.Println(err)
		}
		switch r.Method {
		case http.MethodPost:
			upload(p, w, fileBytes, dir)
		case http.MethodGet:
			w.Write([]byte("{success:0, error:get}"))
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func upload(path string, w http.ResponseWriter, reqBody []byte, dir string) {
	EnsureDir(path)
	f, err := os.Create(path)
	if err != nil {
		log.Println(err)
		w.Write([]byte("{success:0, error:'create file fail'}"))
	}
	defer f.Close()
	f.Write(reqBody)
	d := strings.Split(dir, "/")
	name := d[len(d)-1]
	finfo := file{
		"/f" + dir, // TODO Upload Using Cookie and Prefix ... ....
		name,
		len(reqBody),
	}
	j, err2 := json.Marshal(ret{1, finfo})
	if err2 == nil {
		w.Write(j)
	} else {
		w.Write([]byte("{success:0, error:'json fail'}"))
	}
	log.Printf("Writing %s\n", path)
}

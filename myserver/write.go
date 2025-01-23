package myserver

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func EnsureDir(fileName string) {
	dirName := filepath.Dir(fileName)
	if _, serr := os.Stat(dirName); serr != nil {
		merr := os.MkdirAll(dirName, os.ModePerm)
		if merr != nil {
			//panic(merr)
		}
	}
}

func CreateWriteHandleFunc(root string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		dir := r.URL.Path
		switch r.Method {
		case http.MethodPost:
			log.Println("in method post")
			p := root + dir
			write(p, w, r, dir)
		case http.MethodGet:
			w.Write([]byte("Post data only"))
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}

	}
}

/*
		responseJson.success = 1;
        responseJson.file = {
          url: image.path,
          name: image.name,
          size: image.size
        };
*/
type file struct {
	Url  string `json:"url"`
	Name string `json:"name"`
	Size int    `json:"size"`
}
type ret struct {
	Success int  `json:"success"`
	File    file `json:"file"`
}
type errRet struct {
	Success int    `json:"success"`
	Error   string `json:"error"`
}

func write(path string, w http.ResponseWriter, r *http.Request, dir string) {
	reqBody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Fatal(err)
	}
	EnsureDir(path)
	f, err := os.Create(path)
	if err != nil {
		log.Println(err)
		w.Write([]byte("{success:0}"))
	}
	defer f.Close()
	f.Write(reqBody)
	d := strings.Split(dir, "/")
	name := d[len(d)-1]
	// TODO with url prefix problem
	// TODO merge with upload
	finfo := file{
		"/f" + dir,
		name,
		len(reqBody),
	}
	j, err2 := json.Marshal(ret{1, finfo})
	if err2 == nil {
		w.Write(j)
	} else {
		w.Write([]byte("{success:0, error:1}"))
	}
	//w.Write([]byte("{ success:1, status:'done'}"))
	log.Printf("Writing %s\n", path)
}

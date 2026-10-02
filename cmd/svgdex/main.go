// svgdex is a searchable catalogue of SVG icons, served read-only over HTTP.
//
//	svgdex index [-set ID] [-name N] [-license L] [-author A] [-url U] FOLDER…   (re)import folders, one set each
//	svgdex drawio [-set ID] [-name N] [-license L] [-author A] [-url U] FILE…  (re)import draw.io libraries: one set
//	                                                                           each, or all into -set (file = category)
//	svgdex remove SET…                                                         drop sets (and their unused files)
//	svgdex sets                                                                list the sets
//	svgdex search [-set ID] [-style mono|color] [-license L] WORDS…            search from the terminal
//	svgdex serve [-p PORT] [-global] [-cors ORIGINS]                            the HTTP API + a test page at /
//
// Every command takes -d DIR, the catalogue's folder (default $SVGDEX_HOME, else ~/.svgdex): DIR/index.db and the
// SVGs by content hash in DIR/svg/. A folder may carry a set.json (name, licence, author, url, per-icon names and
// tags); see svgdex.FolderSet. The API: GET /search /suggest /sets /icon/<id> /svg/<hash>.svg — see svgdex.Handler.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/svgdex"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: svgdex <command> [flags] [args]

  index  [-set ID] [-name N] [-license L] [-author A] [-url U] FOLDER…   (re)import folders, one set each
  drawio [-set ID] [-name N] [-license L] [-author A] [-url U] FILE…   (re)import draw.io libraries (.xml):
                                                                      one set each — or, with -set, all into one
                                                                      (each file's name is its icons' category)
  remove SET…                                                         drop sets
  sets                                                                list the sets
  search [-set ID] [-style mono|color] [-license L] WORDS…            search
  serve  [-p PORT] [-global] [-cors ORIGINS]                          serve the API (+ a test page at /)

every command: -d DIR  the catalogue (default $SVGDEX_HOME, else ~/.svgdex)`)
	os.Exit(2)
}

func defaultDir() string {
	if d := os.Getenv("SVGDEX_HOME"); d != "" {
		return d
	}
	return "~/.svgdex"
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("d", defaultDir(), "the catalogue's folder")
	open := func() *svgdex.Store {
		st, err := svgdex.Open(*dir)
		if err != nil {
			log.Fatal(err)
		}
		return st
	}
	switch cmd {
	case "index":
		var set svgdex.Set
		fs.StringVar(&set.ID, "set", "", "the set's id (one folder only; default: set.json, else the folder's name)")
		fs.StringVar(&set.Name, "name", "", "the set's name")
		fs.StringVar(&set.License, "license", "", "the set's licence (SPDX id, e.g. CC-BY-4.0, MIT)")
		fs.StringVar(&set.Author, "author", "", "the set's author")
		fs.StringVar(&set.URL, "url", "", "the set's homepage")
		fs.BoolVar(&set.Replace, "replace", false, "replace a set of the same id imported from somewhere else")
		fs.Parse(args)
		if fs.NArg() == 0 {
			usage()
		}
		if fs.NArg() > 1 && (set.ID != "" || set.Name != "") {
			log.Fatal("-set / -name name ONE set: give one folder")
		}
		st := open()
		defer st.Close()
		for _, f := range fs.Args() {
			t := time.Now()
			rep, err := st.ImportFolder(f, set)
			if err != nil {
				log.Fatalf("%s: %v", f, err)
			}
			if rep.Added == 0 {
				fmt.Printf("%s: nothing to import (%d skipped)\n", rep.Set, len(rep.Skipped))
			} else {
				fmt.Printf("%s: %d icons (%s)\n", rep.Set, rep.Added, time.Since(t).Round(time.Millisecond))
			}
			for _, s := range rep.Skipped {
				fmt.Printf("  skipped %s\n", s)
			}
		}
		if n, err := st.Prune(); err == nil && n > 0 {
			fmt.Printf("removed %d unused file(s)\n", n)
		}
	case "drawio":
		var set svgdex.Set
		fs.StringVar(&set.ID, "set", "", "import every file into this ONE set (each file's name is its icons' category)")
		fs.StringVar(&set.Name, "name", "", "the set's name")
		fs.StringVar(&set.License, "license", "", "the set's licence (icons whose title names one keep theirs)")
		fs.StringVar(&set.Author, "author", "", "the set's author")
		fs.StringVar(&set.URL, "url", "", "the set's homepage")
		fs.BoolVar(&set.Replace, "replace", false, "replace a set of the same id imported from somewhere else")
		fs.Parse(args)
		if fs.NArg() == 0 {
			usage()
		}
		st := open()
		defer st.Close()
		groups := [][]string{fs.Args()}
		if set.ID == "" {
			if set.Name != "" && fs.NArg() > 1 {
				log.Fatal("-name names ONE set: give -set too, or one file")
			}
			groups = nil
			for _, f := range fs.Args() {
				groups = append(groups, []string{f})
			}
		}
		failed := 0
		for _, files := range groups {
			t := time.Now()
			rep, err := st.ImportDrawio(files, set)
			if err != nil { // one file's problem (e.g. its id is taken): say so, go on with the others
				log.Print(err)
				failed++
				continue
			}
			extra := ""
			if rep.Unsupported > 0 {
				extra = fmt.Sprintf(", %d draw.io shape(s) not imported", rep.Unsupported)
			}
			if rep.Added == 0 {
				fmt.Printf("%s: nothing to import%s\n", rep.Set, extra)
			} else {
				fmt.Printf("%s: %d icons (%s)%s\n", rep.Set, rep.Added, time.Since(t).Round(time.Millisecond), extra)
			}
			for _, s := range rep.Skipped {
				fmt.Printf("  skipped %s\n", s)
			}
		}
		if n, err := st.Prune(); err == nil && n > 0 {
			fmt.Printf("removed %d unused file(s)\n", n)
		}
		if failed > 0 {
			os.Exit(1)
		}
	case "remove":
		fs.Parse(args)
		st := open()
		defer st.Close()
		for _, id := range fs.Args() {
			if err := st.RemoveSet(id); err != nil {
				log.Fatal(err)
			}
		}
		n, _ := st.Prune()
		fmt.Printf("removed %d set(s), %d file(s)\n", fs.NArg(), n)
	case "sets":
		fs.Parse(args)
		st := open()
		defer st.Close()
		sets, err := st.Sets()
		if err != nil {
			log.Fatal(err)
		}
		for _, s := range sets {
			fmt.Printf("%-24s %6d  %-12s %s\n", s.ID, s.Count, s.License, s.Name)
		}
	case "search":
		var q svgdex.Query
		var sets string
		fs.StringVar(&sets, "set", "", "only these sets (comma-separated)")
		fs.StringVar(&q.Style, "style", "", "mono | color")
		fs.StringVar(&q.License, "license", "", "only this licence")
		fs.IntVar(&q.Limit, "n", 20, "how many")
		fs.Parse(args)
		q.Q = strings.Join(fs.Args(), " ")
		if sets != "" {
			q.Sets = strings.Split(sets, ",")
		}
		st := open()
		defer st.Close()
		res, err := st.Search(q)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%d match(es)\n", res.Total)
		for _, ic := range res.Items {
			fmt.Printf("%-6d %-28s %-16s %-12s %s\n", ic.ID, ic.Name, ic.Set, ic.License, ic.Key)
		}
	case "serve":
		port := fs.Int("p", 8434, "port")
		global := fs.Bool("global", false, "listen on all interfaces (default: 127.0.0.1 only)")
		var origins myserver.OriginList
		fs.Var(&origins, "cors", "browser origins allowed to call the API (default: any — it is public and read-only)")
		quiet := fs.Bool("q", false, "no request log")
		fs.Parse(args)
		st := open()
		defer st.Close()
		opt := svgdex.ServerOptions{Origins: origins}
		if !*quiet {
			opt.Log = log.Printf
		}
		host := "127.0.0.1"
		if *global {
			host = ""
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(*port)))
		if err != nil {
			log.Fatal(err)
		}
		sets, _ := st.Sets()
		total := 0
		for _, s := range sets {
			total += s.Count
		}
		log.Printf("svgdex: %d icons in %d set(s) from %s — http://%s/", total, len(sets), st.Dir(), ln.Addr())
		srv := &http.Server{Handler: svgdex.Handler(st, opt), ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.Serve(ln))
	default:
		usage()
	}
}

// myweb serves a folder of data files to web apps, with a file manager and a
// terminal UI. See README.md.
package main

import (
	"os"

	"github.com/nimezhu/myweb/cli"
)

func main() {
	os.Exit(cli.Serve(cli.Spec{Name: "myweb", Args: os.Args[1:]}))
}

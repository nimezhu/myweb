package cli

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/internal/fsx"
	"github.com/nimezhu/myweb/myserver"
)

// "backup" and "restore" keep a home and its data folder in one .zip:
//
//	backup [FILE.zip] [-no-files] [-home DIR]
//	restore FILE.zip [-d DIR] [-force] [-dry-run] [-home DIR]
//
// The zip holds backup.json, home/ (config, apps, catalogs; not run/) and
// data/: the data folder with its .myweb (versions, trash, share links), or
// with -no-files only its .myweb. Symbolic links are left out. restore never
// replaces a file that differs unless -force, and refuses while a server
// runs.

// backupInfo is backup.json.
type backupInfo struct {
	Program string    `json:"program"`
	Version string    `json:"version"`
	Created time.Time `json:"created"`
	Home    string    `json:"home"`
	Data    string    `json:"data"`
	// DataInHome: the data folder was the home's own (home/data), so a
	// restore puts it in the new home's
	DataInHome bool `json:"dataInHome"`
	NoFiles    bool `json:"noFiles,omitempty"`
}

const backupInfoName = "backup.json"

// dataFolder is the data folder a plain serve uses: -d from the config, or
// the home's own.
func dataFolder(spec Spec, dirs home.Dirs) (string, error) {
	fs, sf := newServeFlags(spec)
	fs.SetOutput(io.Discard)
	cfg, err := loadConfig(dirs)
	if err != nil {
		return "", err
	}
	if err := cfg.apply(fs, configFile(dirs)); err != nil {
		return "", err
	}
	if *sf.directory == "" {
		return dirs.Data(), nil
	}
	return filepath.Abs(expandHome(*sf.directory))
}

func backupCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "backup")
	noFiles := fl.Bool("no-files", false, "")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{errors.New("backup takes at most one FILE")}
	}
	dirs, err := homeFor(spec.Name, *fl.home)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dirs.Root); err != nil {
		return fmt.Errorf("nothing to back up: %s doesn't exist", tildePath(dirs.Root))
	}
	data, err := dataFolder(spec, dirs)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	out := fmt.Sprintf("%s-backup-%s.zip", spec.Name, time.Now().Format("20060102-150405"))
	if len(pos) == 1 {
		out = pos[0]
	}
	if out, err = filepath.Abs(expandHome(out)); err != nil {
		return err
	}
	info := backupInfo{Program: spec.Name, Version: myserver.VERSION, Created: time.Now().UTC().Truncate(time.Second),
		Home: dirs.Root, Data: data, DataInHome: data == dirs.Data(), NoFiles: *noFiles}
	if len(runningServers(dirs)) > 0 {
		fmt.Fprintf(w, "(a %s is running: files it changes meanwhile may be left out or copied half-written)\n", spec.Name)
	}

	tmp, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.Chmod(0o600) // the config may hold the token
	zw := zip.NewWriter(tmp)
	b, _ := json.MarshalIndent(info, "", "  ")
	if f, err := zw.CreateHeader(&zip.FileHeader{Name: backupInfoName, Method: zip.Deflate, Modified: info.Created}); err == nil {
		f.Write(append(b, '\n'))
	}
	var st backupStats
	skip := func(p string) bool { return p == out || p == tmp.Name() } // not into the backup
	skipHome := func(p string) bool {
		return skip(p) || p == filepath.Join(dirs.Root, "run") || (p == data && info.DataInHome)
	}
	if err := addTree(zw, dirs.Root, "home", skipHome, &st); err != nil {
		return err
	}
	if fi, err := os.Stat(data); err == nil && fi.IsDir() {
		src := data
		if *noFiles {
			src = filepath.Join(data, ".myweb")
		}
		dst := "data"
		if *noFiles {
			dst = "data/.myweb"
		}
		if _, err := os.Stat(src); err == nil {
			skipData := func(p string) bool { return skip(p) || p == dirs.Root } // a data folder around the home: it's in home/
			if err := addTree(zw, src, dst, skipData, &st); err != nil {
				return err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := fsx.Rename(tmp.Name(), out); err != nil {
		return err
	}
	fi, _ := os.Stat(out)
	fmt.Fprintf(w, "backed up %s and %s: %d files, %s, into %s\n", tildePath(dirs.Root), tildePath(data), st.files, sizeText(st.bytes), tildePath(out))
	if st.links > 0 {
		fmt.Fprintf(w, "(%d symbolic link%s left out)\n", st.links, plural(st.links))
	}
	if fi != nil {
		fmt.Fprintf(w, "the zip is %s; it holds the token, keep it private\n", sizeText(fi.Size()))
	}
	return nil
}

type backupStats struct {
	files, links int
	bytes        int64
}

// addTree adds the files under root to zw, at prefix/<their path>.
func addTree(zw *zip.Writer, root, prefix string, skip func(string) bool, st *backupStats) error {
	return filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skip(p) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		name := path.Join(prefix, filepath.ToSlash(rel))
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			st.links++
			return nil
		case e.IsDir():
			if p == root {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			h, _ := zip.FileInfoHeader(info)
			h.Name = name + "/"
			_, err = zw.CreateHeader(h)
			return err
		case !e.Type().IsRegular():
			return nil // a socket or device
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name, h.Method = name, zip.Deflate
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		dst, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		n, err := io.Copy(dst, f)
		st.files++
		st.bytes += n
		return err
	})
}

func restoreCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "restore")
	force := fl.Bool("force", false, "")
	dry := fl.Bool("dry-run", false, "")
	dataDir := fl.String("d", "", "")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{errors.New("restore takes the backup's FILE.zip")}
	}
	dirs, err := homeFor(spec.Name, *fl.home)
	if err != nil {
		return err
	}
	if n := len(runningServers(dirs)); n > 0 && !*dry {
		return fmt.Errorf("a %s is running from this home; stop it first (%s stop -all)", spec.Name, spec.Name)
	}
	zr, err := zip.OpenReader(expandHome(pos[0]))
	if err != nil {
		return err
	}
	defer zr.Close()
	var info backupInfo
	for _, f := range zr.File {
		if f.Name == backupInfoName {
			rc, err := f.Open()
			if err == nil {
				err = json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&info)
				rc.Close()
			}
			if err != nil {
				return fmt.Errorf("%s: %v", backupInfoName, err)
			}
		}
	}
	if info.Program == "" {
		return fmt.Errorf("%s is not a %s backup (no %s)", pos[0], spec.Name, backupInfoName)
	}
	if info.Program != spec.Name {
		return fmt.Errorf("a backup of %s, not %s", info.Program, spec.Name)
	}
	data := info.Data
	switch {
	case *dataDir != "":
		data, err = filepath.Abs(expandHome(*dataDir))
		if err != nil {
			return err
		}
	case info.DataInHome:
		data = dirs.Data()
	}
	fmt.Fprintf(w, "a backup of %s %s from %s\n", info.Program, info.Version, info.Created.Local().Format("2006-01-02 15:04"))
	fmt.Fprintf(w, "  home → %s\n  data → %s\n", tildePath(dirs.Root), tildePath(data))

	var st struct{ wrote, same, differ, skipped int }
	var differ []string
	for _, f := range zr.File {
		if f.Name == backupInfoName {
			continue
		}
		dst, ok := restoreTarget(f.Name, dirs.Root, data)
		if !ok {
			st.skipped++
			continue
		}
		if f.FileInfo().IsDir() {
			if !*dry {
				if err := os.MkdirAll(dst, 0o755); err != nil {
					return err
				}
			}
			continue
		}
		if !f.Mode().IsRegular() {
			st.skipped++
			continue
		}
		if fi, err := os.Lstat(dst); err == nil {
			if same, err := sameContent(f, dst, fi); err == nil && same {
				st.same++
				continue
			}
			if !*force {
				st.differ++
				differ = append(differ, tildePath(dst))
				continue
			}
		}
		st.wrote++
		if *dry {
			continue
		}
		if err := restoreFile(f, dst); err != nil {
			return fmt.Errorf("%s: %w", dst, err)
		}
	}
	verb := "restored"
	if *dry {
		verb = "would restore"
	}
	fmt.Fprintf(w, "%s %d file%s; %d already there\n", verb, st.wrote, plural(st.wrote), st.same)
	if st.skipped > 0 {
		fmt.Fprintf(w, "%d entr%s left out (outside home/ and data/, run/, or not files)\n", st.skipped, map[bool]string{true: "y", false: "ies"}[st.skipped == 1])
	}
	if st.differ > 0 {
		if st.differ == 1 {
			fmt.Fprintf(w, "1 file differs from the backup and was kept as it is (-force replaces it):\n")
		} else {
			fmt.Fprintf(w, "%d files differ from the backup and were kept as they are (-force replaces them):\n", st.differ)
		}
		for i, p := range differ {
			if i == 10 {
				fmt.Fprintf(w, "  … and %d more\n", len(differ)-10)
				break
			}
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if !info.DataInHome && *dataDir == "" && !*dry {
		fmt.Fprintf(w, "(the data folder is where it was; serve another with -d, or restore with -d DIR)\n")
	}
	return nil
}

// restoreTarget is where a zip entry goes: home/… into the home (not run/),
// data/… into the data folder. ok is false for anything else, and for
// names that would leave them.
func restoreTarget(name, homeRoot, data string) (string, bool) {
	if strings.Contains(name, "\\") || path.IsAbs(name) {
		return "", false
	}
	clean := path.Clean(name)
	if clean != strings.TrimSuffix(name, "/") || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false
	}
	top, rest, _ := strings.Cut(clean, "/")
	if rest == "" {
		return "", false
	}
	switch top {
	case "home":
		if rest == "run" || strings.HasPrefix(rest, "run/") {
			return "", false
		}
		return filepath.Join(homeRoot, filepath.FromSlash(rest)), true
	case "data":
		return filepath.Join(data, filepath.FromSlash(rest)), true
	}
	return "", false
}

// sameContent reports whether the file at p (fi) holds what f does.
func sameContent(f *zip.File, p string, fi os.FileInfo) (bool, error) {
	if !fi.Mode().IsRegular() || uint64(fi.Size()) != f.UncompressedSize64 {
		return false, nil
	}
	hash := func(r io.Reader) ([]byte, error) {
		h := sha256.New()
		_, err := io.Copy(h, r)
		return h.Sum(nil), err
	}
	rc, err := f.Open()
	if err != nil {
		return false, err
	}
	defer rc.Close()
	a, err := hash(rc)
	if err != nil {
		return false, err
	}
	lf, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer lf.Close()
	b, err := hash(lf)
	return err == nil && bytes.Equal(a, b), err
}

// restoreFile writes f to dst whole (a temporary file renamed into place),
// with its permissions (no more than rwx) and modification time.
func restoreFile(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".restore*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	mode := f.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	os.Chmod(tmp.Name(), mode)
	if err := fsx.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	if t := f.Modified; !t.IsZero() {
		os.Chtimes(dst, t, t)
	}
	return nil
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

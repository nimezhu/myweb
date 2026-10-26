package cli

// Automatic backups: a server with -backup-every (from the config: "backup
// -every 1d" puts it there) makes a backup when the newest one is that old,
// into -backup-to (default <home>/backups), and keeps the newest
// -backup-keep of them. Several servers of one home take turns by a lock
// file; auto-backup.json there says how the last one went (doctor reads it).

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
)

var (
	autoBackupDelay = time.Minute      // after a start, before the first check
	autoBackupCheck = 10 * time.Minute // between checks
	backupLockStale = 6 * time.Hour    // a lock this old was left by a crash
)

// every is a flag value: how often, as 30m, 12h, 1d or 1w; "off" or "" is
// never.
type every time.Duration

func (e *every) String() string {
	d := time.Duration(*e)
	switch {
	case d <= 0:
		return "off"
	case d%(7*24*time.Hour) == 0:
		return strconv.Itoa(int(d/(7*24*time.Hour))) + "w"
	case d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return d.String()
}

func (e *every) Set(v string) error {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" || v == "off" || v == "0" {
		*e = 0
		return nil
	}
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, suffix)); err == nil && strings.HasSuffix(v, suffix) {
			if n <= 0 {
				break
			}
			*e = every(time.Duration(n) * unit)
			return nil
		}
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return errors.New("give a time like 1d, 12h, 1w, or off")
	}
	if d < 10*time.Minute {
		return errors.New("at most every 10m")
	}
	*e = every(d)
	return nil
}

// configured parses the config's values of serve's flags (no command line).
func configured(spec Spec, dirs home.Dirs) (*serveFlags, error) {
	fs, sf := newServeFlags(spec)
	fs.SetOutput(io.Discard)
	cfg, err := loadConfig(dirs)
	if err != nil {
		return nil, err
	}
	if err := cfg.apply(fs, configFile(dirs)); err != nil {
		return nil, err
	}
	return sf, nil
}

// backupsDir is where automatic backups go.
func backupsDir(spec Spec, dirs home.Dirs) string {
	if sf, err := configured(spec, dirs); err == nil && *sf.backupTo != "" {
		if p, err := filepath.Abs(expandHome(*sf.backupTo)); err == nil {
			return p
		}
	}
	return filepath.Join(dirs.Root, "backups")
}

func backupsDirFor(dirs home.Dirs, to string) string {
	if to == "" {
		return filepath.Join(dirs.Root, "backups")
	}
	if p, err := filepath.Abs(expandHome(to)); err == nil {
		return p
	}
	return to
}

// autoBackups lists the automatic backups in dir, newest first.
func autoBackups(name, dir string) []os.FileInfo {
	entries, _ := os.ReadDir(dir)
	var out []os.FileInfo
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), name+"-auto-") && strings.HasSuffix(e.Name(), ".zip") {
			if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
				out = append(out, fi)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() > out[j].Name() })
	return out
}

// autoBackupStatus is auto-backup.json in the backups folder.
type autoBackupStatus struct {
	Time  time.Time `json:"time"`
	File  string    `json:"file,omitempty"`
	Size  int64     `json:"size,omitempty"`
	Error string    `json:"error,omitempty"`
}

const autoBackupStatusName = "auto-backup.json"

func readAutoBackupStatus(dir string) (autoBackupStatus, bool) {
	var st autoBackupStatus
	b, err := os.ReadFile(filepath.Join(dir, autoBackupStatusName))
	return st, err == nil && json.Unmarshal(b, &st) == nil
}

type autoBackup struct {
	spec  Spec
	dirs  home.Dirs
	data  string
	dir   string
	every time.Duration
	keep  int
	logf  func(string, ...interface{})
}

// due makes a backup if the newest is older than every. It returns the new
// backup's path ("" if none was due, or another server makes it).
func (a *autoBackup) due(now time.Time) (string, error) {
	if old := autoBackups(a.spec.Name, a.dir); len(old) > 0 && now.Sub(old[0].ModTime()) < a.every {
		return "", nil
	}
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return "", err
	}
	lock := filepath.Join(a.dir, ".backup.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		if fi, err := os.Stat(lock); err == nil && time.Since(fi.ModTime()) < backupLockStale {
			return "", nil // another server is making it
		}
		os.Remove(lock)
		f, err = os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return "", err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Close()
	defer os.Remove(lock)

	out := filepath.Join(a.dir, fmt.Sprintf("%s-auto-%s.zip", a.spec.Name, now.Format("20060102-150405")))
	st := autoBackupStatus{Time: now.UTC().Truncate(time.Second)}
	_, err = makeBackup(a.spec, a.dirs, a.data, out, false, a.dir)
	if err != nil {
		st.Error = err.Error()
	} else {
		st.File = filepath.Base(out)
		if fi, err := os.Stat(out); err == nil {
			st.Size = fi.Size()
		}
		for _, old := range autoBackups(a.spec.Name, a.dir)[min(a.keep, len(autoBackups(a.spec.Name, a.dir))):] {
			os.Remove(filepath.Join(a.dir, old.Name()))
		}
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	os.WriteFile(filepath.Join(a.dir, autoBackupStatusName), append(b, '\n'), 0o600)
	if err != nil {
		return "", err
	}
	return out, nil
}

// start checks now and then, for as long as the program runs.
func (a *autoBackup) start() {
	go func() {
		time.Sleep(autoBackupDelay)
		for {
			if out, err := a.due(time.Now()); err != nil {
				a.logf("backup failed: %v", err)
			} else if out != "" {
				a.logf("backed up into %s", tildePath(out))
			}
			time.Sleep(autoBackupCheck)
		}
	}()
}

// scheduleBackups is "backup -every": it sets the schedule in the config.
func scheduleBackups(spec Spec, dirs home.Dirs, ev *every, keep int, to string, w io.Writer) error {
	c, err := loadConfig(dirs)
	if err != nil {
		return err
	}
	if *ev == 0 {
		delete(c, "backup-every")
		if err := c.save(dirs); err != nil {
			return err
		}
		fmt.Fprintf(w, "automatic backups are off (for servers started from now on)\n")
		return nil
	}
	c["backup-every"] = ev.String()
	if keep > 0 {
		c["backup-keep"] = strconv.Itoa(keep)
	}
	if to != "" {
		c["backup-to"] = to
	}
	if err := c.save(dirs); err != nil {
		return err
	}
	sf, err := configured(spec, dirs)
	if err != nil {
		return err
	}
	dir := backupsDirFor(dirs, *sf.backupTo)
	fmt.Fprintf(w, "a running %s backs up every %s into %s, keeping the newest %d\n", spec.Name, ev, tildePath(dir), *sf.backupKeep)
	if strings.HasPrefix(dir+string(filepath.Separator), dirs.Root+string(filepath.Separator)) {
		fmt.Fprintf(w, "(that is on this disk: -to a folder on another disk also keeps them if this one fails)\n")
	}
	if len(runningServers(dirs)) > 0 {
		fmt.Fprintf(w, "servers running now start doing it once restarted (%s stop, then start it; a service: %s doctor -fix)\n", spec.Name, spec.Name)
	}
	return nil
}

// listBackups is "backup -list".
func listBackups(spec Spec, dirs home.Dirs, w io.Writer) error {
	sf, err := configured(spec, dirs)
	if err != nil {
		return err
	}
	dir := backupsDirFor(dirs, *sf.backupTo)
	ev := every(*sf.backupEvery)
	if ev == 0 {
		fmt.Fprintf(w, "automatic backups are off (%s backup -every 1d turns them on)\n", spec.Name)
	} else {
		fmt.Fprintf(w, "every %s into %s, keeping %d\n", &ev, tildePath(dir), *sf.backupKeep)
	}
	if st, ok := readAutoBackupStatus(dir); ok && st.Error != "" {
		fmt.Fprintf(w, "the last one failed %s: %s\n", st.Time.Local().Format("2006-01-02 15:04"), st.Error)
	}
	for _, fi := range autoBackups(spec.Name, dir) {
		fmt.Fprintf(w, "  %s  %8s  %s\n", fi.ModTime().Format("2006-01-02 15:04"), sizeText(fi.Size()), filepath.Join(dir, fi.Name()))
	}
	return nil
}

var _ flag.Value = (*every)(nil)

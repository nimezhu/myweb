package cli

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
)

// "tls" shows the CA that -https's certificates are signed with, and trusts
// it on this machine (other devices: /_myweb/cert of a running server):
//
//	tls                     the CA, what it may sign, whether this machine trusts it
//	tls trust [-print]      add it to this machine's trusted certificates
//	tls untrust [-print]    take it out again

func tlsCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "tls")
	printOnly := fl.Bool("print", false, "")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	dirs, err := homeFor(spec.Name, *fl.home)
	if err != nil {
		return err
	}
	sub := ""
	if len(pos) > 0 {
		sub = pos[0]
	}
	if len(pos) > 1 || (sub != "" && sub != "trust" && sub != "untrust") {
		return usageError{errors.New("tls takes nothing, trust or untrust")}
	}
	if _, _, _, err := loadTLS(dirs, "", "", false); err != nil { // makes the CA if there is none yet
		return err
	}
	ca, err := readCert(caFile(dirs))
	if err != nil {
		return err
	}
	if sub == "" {
		sum := sha256.Sum256(ca.Raw)
		fmt.Fprintf(w, "CA          %s\n", tildePath(caFile(dirs)))
		fmt.Fprintf(w, "name        %s\n", ca.Subject.CommonName)
		fmt.Fprintf(w, "SHA-256     %s\n", fingerprint(hex.EncodeToString(sum[:])))
		fmt.Fprintf(w, "may sign    %s (and any address)\n", strings.Join(ca.PermittedDNSDomains, ", "))
		fmt.Fprintf(w, "ends        %s\n", ca.NotAfter.Local().Format("2 Jan 2006"))
		if trustedHere(dirs) {
			fmt.Fprintf(w, "this machine trusts it\n")
		} else {
			fmt.Fprintf(w, "this machine doesn't trust it yet: %s tls trust\n", spec.Name)
		}
		fmt.Fprintf(w, "\nother devices: open https://<this machine>:PORT%s while %s -https runs\n", myserver.CertPath, spec.Name)
		return nil
	}
	home, _ := os.UserHomeDir()
	cmds, notes, err := trustCommands(runtime.GOOS, caFile(dirs), ca, sub == "trust", home, hasCommand, dirExists)
	if err != nil {
		return err
	}
	for _, c := range cmds {
		fmt.Fprintln(w, shellQuote(c))
		if *printOnly {
			continue
		}
		if err := runTrust(c); err != nil {
			return fmt.Errorf("%s: %v", c[0], err)
		}
	}
	if !*printOnly {
		if sub == "trust" {
			fmt.Fprintf(w, "\nthis machine trusts %s now (browsers that are open may need a restart)\n", ca.Subject.CommonName)
		} else {
			fmt.Fprintf(w, "\nthis machine no longer trusts %s\n", ca.Subject.CommonName)
		}
	}
	for _, n := range notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	return nil
}

// runTrust runs one command of trust/untrust, at the terminal (sudo and the
// system may ask); replaced in tests.
var runTrust = func(c []string) error {
	cmd := exec.Command(c[0], c[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func hasCommand(name string) bool { _, err := exec.LookPath(name); return err == nil }

func dirExists(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// trustedHere reports whether this machine's trusted certificates make the
// server's certificate (cert.pem) valid for localhost.
func trustedHere(dirs home.Dirs) bool {
	c, err := readCert(filepath.Join(tlsDir(dirs), "cert.pem"))
	if err != nil {
		return false
	}
	_, err = c.Verify(x509.VerifyOptions{DNSName: "localhost", CurrentTime: time.Now()})
	return err == nil
}

// linuxStores are the system's trusted-certificate folders, with the command
// that applies a change.
var linuxStores = []struct{ dir, update string }{
	{"/usr/local/share/ca-certificates", "update-ca-certificates"}, // Debian, Ubuntu, Alpine
	{"/etc/pki/ca-trust/source/anchors", "update-ca-trust"},        // Fedora, RHEL
	{"/etc/ca-certificates/trust-source/anchors", "trust"},         // Arch
	{"/usr/share/pki/trust/anchors", "update-ca-certificates"},     // openSUSE
}

// trustCommands are the commands that add (or remove) the CA at file to
// this machine's trusted certificates on goos, and notes on what they
// don't cover.
func trustCommands(goos, file string, ca *x509.Certificate, add bool, home string, has func(string) bool, isDir func(string) bool) ([][]string, []string, error) {
	sha := sha1.Sum(ca.Raw)
	sha1hex := strings.ToUpper(hex.EncodeToString(sha[:]))
	nick := ca.Subject.CommonName
	var cmds [][]string
	var notes []string
	switch goos {
	case "darwin":
		keychain := filepath.Join(home, "Library/Keychains/login.keychain-db")
		if add {
			cmds = append(cmds, []string{"security", "add-trusted-cert", "-r", "trustRoot", "-k", keychain, file})
		} else {
			cmds = append(cmds, []string{"security", "remove-trusted-cert", file},
				[]string{"security", "delete-certificate", "-Z", sha1hex, keychain})
		}
		notes = append(notes, "Firefox keeps its own list: Settings › Privacy & Security › Certificates › View Certificates › Authorities")
	case "windows":
		if add {
			cmds = append(cmds, []string{"certutil", "-user", "-addstore", "Root", file})
		} else {
			cmds = append(cmds, []string{"certutil", "-user", "-delstore", "Root", sha1hex})
		}
		notes = append(notes, "Firefox keeps its own list: Settings › Privacy & Security › Certificates › View Certificates › Authorities")
	case "linux":
		sudo := []string{}
		if os.Geteuid() != 0 {
			if !has("sudo") {
				return nil, nil, errors.New("changing the system's certificates needs root: run this as root")
			}
			sudo = []string{"sudo"}
		}
		found := false
		for _, st := range linuxStores {
			if !isDir(st.dir) || !has(st.update) {
				continue
			}
			found = true
			dest := filepath.Join(st.dir, "myweb-"+strings.ToLower(sha1hex[:12])+".crt")
			update := []string{st.update}
			switch st.update {
			case "update-ca-trust":
				update = append(update, "extract")
			case "trust":
				update = append(update, "extract-compat")
			case "update-ca-certificates":
				if !add {
					update = append(update, "--fresh")
				}
			}
			if add {
				cmds = append(cmds, append(append([]string{}, sudo...), "install", "-m", "0644", file, dest))
			} else {
				cmds = append(cmds, append(append([]string{}, sudo...), "rm", "-f", dest))
			}
			cmds = append(cmds, append(append([]string{}, sudo...), update...))
			break
		}
		if !found {
			return nil, nil, errors.New("found no system certificate store this knows (Debian, Fedora, Arch, openSUSE); trust " + file + " the way your system does")
		}
		nss := filepath.Join(home, ".pki/nssdb")
		switch {
		case isDir(nss) && has("certutil"):
			if add {
				cmds = append(cmds, []string{"certutil", "-d", "sql:" + nss, "-A", "-t", "C,,", "-n", nick, "-i", file})
			} else {
				cmds = append(cmds, []string{"certutil", "-d", "sql:" + nss, "-D", "-n", nick})
			}
		case isDir(nss):
			notes = append(notes, "Chrome on Linux keeps its own list (~/.pki/nssdb): install certutil (libnss3-tools or nss-tools) and run this again, or import "+file+" in Chrome's settings › Privacy and security › Security › Manage certificates")
		}
		notes = append(notes, "Firefox keeps its own list: Settings › Privacy & Security › Certificates › View Certificates › Authorities")
	default:
		return nil, nil, fmt.Errorf("trusting a certificate on %s isn't known here; trust %s the way your system does", goos, file)
	}
	return cmds, notes, nil
}

// shellQuote shows a command as one would type it.
func shellQuote(c []string) string {
	out := make([]string, len(c))
	for i, a := range c {
		if a == "" || strings.ContainsAny(a, " '\"$`\\()&;|<>*?!") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

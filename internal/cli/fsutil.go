package cli

import (
	"archive/tar"
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ulikunitz/xz"
)

// owner is a parsed --chown user:group.
type owner struct{ uid, gid int }

// parseChown accepts uid:gid, as the consumers pass it (`id -u`:`id -g`),
// and also user and group names, as chown(1) did.
func parseChown(s string) (*owner, error) {
	if s == "" {
		return nil, nil
	}
	u, g, _ := strings.Cut(s, ":")
	o := &owner{uid: -1, gid: -1}
	if u != "" {
		n, err := strconv.Atoi(u)
		if err != nil {
			usr, lerr := user.Lookup(u)
			if lerr != nil {
				return nil, fmt.Errorf("--chown: unknown user %q", u)
			}
			n, _ = strconv.Atoi(usr.Uid)
			if g == "" && !strings.Contains(s, ":") {
				o.gid, _ = strconv.Atoi(usr.Gid)
			}
		}
		o.uid = n
	}
	if g != "" {
		n, err := strconv.Atoi(g)
		if err != nil {
			grp, lerr := user.LookupGroup(g)
			if lerr != nil {
				return nil, fmt.Errorf("--chown: unknown group %q", g)
			}
			n, _ = strconv.Atoi(grp.Gid)
		}
		o.gid = n
	}
	return o, nil
}

func (o *owner) chown(path string) error {
	if o == nil {
		return nil
	}
	return os.Lchown(path, o.uid, o.gid)
}

// chownAll changes the owner of each path and, for a directory, of
// everything under it (chown -R).
func (o *owner) chownAll(root string, paths ...string) error {
	if o == nil {
		return nil
	}
	for _, p := range paths {
		err := filepath.WalkDir(filepath.Join(root, p), func(q string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Lchown does not follow a symlink, so a link in the tree cannot
			// send the change outside it.
			return os.Lchown(q, o.uid, o.gid) //nolint:gosec // see above
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// askProceed asks to continue unless force is set. End of input aborts, so
// a script without -f fails instead of looping on an empty answer.
func askProceed(force bool, in io.Reader, out io.Writer) error {
	if force {
		return nil
	}
	r := bufio.NewReader(in)
	for {
		_, _ = fmt.Fprint(out, "Continue (y/n)? ")
		line, err := r.ReadString('\n')
		ans := strings.TrimSpace(line)
		if err != nil && ans == "" {
			return errors.New("no input on stdin; aborting (use -f to skip this prompt)")
		}
		switch ans {
		case "y", "Y":
			_, _ = fmt.Fprintln(out, "proceeding ...")
			return nil
		case "n", "N":
			return errors.New("exiting")
		default:
			_, _ = fmt.Fprintln(out, "invalid response")
		}
	}
}

var caKeyPlaceholder = regexp.MustCompile(`CA(\d+)_PRIVATE_KEY`)

// replacePrivateKey writes docker-compose-e2e.yaml from its template, with
// each CA<i>_PRIVATE_KEY replaced by the name of org i's CA key file, which
// cryptogen names after the key. It fills every placeholder the template
// has; the shell version filled org 1 to the --org-count of generate, and
// to 2 for extend, whatever the network had.
func replacePrivateKey(dir, domain string) error {
	b, err := os.ReadFile(filepath.Join(dir, "docker-compose-e2e-template.yaml")) //nolint:gosec // project file
	if err != nil {
		return err
	}
	var ferr error
	out := caKeyPlaceholder.ReplaceAllFunc(b, func(m []byte) []byte {
		i := caKeyPlaceholder.FindSubmatch(m)[1]
		pattern := filepath.Join(dir, "crypto-config", "peerOrganizations", "org"+string(i)+"."+domain, "ca", "*_sk")
		keys, err := filepath.Glob(pattern)
		if err == nil && len(keys) != 1 {
			err = fmt.Errorf("want one CA key matching %s, found %d", pattern, len(keys))
		}
		if err != nil {
			ferr = errors.Join(ferr, err)
			return m
		}
		return []byte(filepath.Base(keys[0]))
	})
	if ferr != nil {
		return ferr
	}
	return os.WriteFile(filepath.Join(dir, "docker-compose-e2e.yaml"), out, 0o644) //nolint:gosec // compose file
}

// writeArchive writes the named paths under dir to a tar.xz at dest.
func writeArchive(dir, dest string, names []string) error {
	f, err := os.Create(dest) //nolint:gosec // operator-supplied path
	if err != nil {
		return err
	}
	if err = writeTarXZ(f, dir, names); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeTarXZ(w io.Writer, dir string, names []string) error {
	xw, err := xz.NewWriter(w)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(xw)
	for _, name := range names {
		err = filepath.WalkDir(filepath.Join(dir, name), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			link := ""
			if info.Mode()&fs.ModeSymlink != 0 {
				if link, err = os.Readlink(p); err != nil {
					return err
				}
			}
			hdr, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			if d.IsDir() {
				hdr.Name += "/"
			}
			if err = tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			src, err := os.Open(p) //nolint:gosec // walked from dir
			if err != nil {
				return err
			}
			defer func() { _ = src.Close() }()
			_, err = io.Copy(tw, src)
			return err
		})
		if err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	return xw.Close()
}

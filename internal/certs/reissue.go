package certs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// ReissueOptions select the leaves to renew and how.
type ReissueOptions struct {
	// Now is the current time; zero means time.Now.
	Now          time.Time
	CryptoConfig string
	// Type is signcert, tls or both.
	Type string
	// Nodes renews the named nodes. A short name (peer1) matches at a
	// dotted boundary, so peer1 does not match peer10.
	Nodes []string
	// Days is the new validity from now, capped at the CA's expiry. Zero
	// means up to the CA's expiry.
	Days       int
	AllExpired bool
	All        bool
	DaysSet    bool
	DryRun     bool
	NoBackup   bool
}

func nodeMatches(node string, patterns []string) bool {
	for _, p := range patterns {
		if node == p || strings.HasPrefix(node, p+".") {
			return true
		}
	}
	return false
}

// Reissue lists every leaf with its expiry and renews the selected ones. It
// writes nothing for a leaf whose CA has expired or whose CA key is missing,
// and it returns an error when any selected leaf was not renewed.
func Reissue(o ReissueOptions, out io.Writer) error {
	if o.DaysSet && o.Days < 1 {
		return errors.New("--days must be a positive integer")
	}
	kinds := []Kind{SignCert, TLS}
	switch o.Type {
	case "", "both":
	case string(SignCert), string(TLS):
		kinds = []Kind{Kind(o.Type)}
	default:
		return fmt.Errorf("--type must be signcert, tls or both, not %q", o.Type)
	}
	leaves, err := Discover(o.CryptoConfig, kinds)
	if err != nil {
		return err
	}
	if len(leaves) == 0 {
		return fmt.Errorf("no leaf certificates found under %s", o.CryptoConfig)
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	// Fail on a mistyped --node rather than renew nothing.
	var unmatched []string
	for _, p := range o.Nodes {
		found := false
		for _, l := range leaves {
			if nodeMatches(l.Node, []string{p}) {
				found = true
				break
			}
		}
		if !found {
			unmatched = append(unmatched, p)
		}
	}
	if len(unmatched) > 0 {
		return fmt.Errorf("no nodes matched --node: %s", strings.Join(unmatched, ", "))
	}

	selecting := o.All || o.AllExpired || len(o.Nodes) > 0
	selected := func(l *Leaf) bool {
		if len(o.Nodes) > 0 && !nodeMatches(l.Node, o.Nodes) {
			return false
		}
		return !o.AllExpired || !l.Expiry.After(now)
	}
	sort.SliceStable(leaves, func(i, j int) bool {
		a, b := leaves[i], leaves[j]
		if a.Org != b.Org {
			return a.Org < b.Org
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		return a.Kind < b.Kind
	})
	var targets []*Leaf
	_, _ = fmt.Fprintf(out, "%-42s %-9s %-22s %s\n", "NODE", "KIND", "EXPIRES", "STATUS")
	for _, l := range leaves {
		status := "ok"
		if !l.Expiry.After(now) {
			status = "EXPIRED"
		}
		if selecting && selected(l) {
			targets = append(targets, l)
			status += " -> reissue"
		}
		_, _ = fmt.Fprintf(out, "%-42s %-9s %-22s %s\n", l.Node, l.Kind, l.Expiry.Format(TimeFormat), status)
	}
	if !selecting {
		_, _ = fmt.Fprint(out, "\nNo targets selected. Re-run with --node NAME, --all-expired, or --all to reissue.\n")
		return nil
	}
	if len(targets) == 0 {
		_, _ = fmt.Fprint(out, "\nNothing matched the selection.\n")
		return nil
	}
	_, _ = fmt.Fprintln(out)
	failures := 0
	for _, l := range targets {
		if err := reissueOne(l, o, now, out); err != nil {
			if !errors.Is(err, ErrReissue) {
				return err
			}
			failures++
			_, _ = fmt.Fprintf(out, "  SKIP %s %s: %s\n", l.Node, l.Kind, strings.TrimPrefix(err.Error(), ErrReissue.Error()+": "))
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d certificate(s) could not be reissued", failures)
	}
	return nil
}

func reissueOne(l *Leaf, o ReissueOptions, now time.Time, out io.Writer) error {
	issuer, err := ResolveCA(l.CADir, l.Cert)
	if err != nil {
		return err
	}
	ca, caKey := issuer.Cert, issuer.Key
	caExpiry := ca.NotAfter.UTC()
	if !caExpiry.After(now) {
		return reissueErrorf("issuing CA expired %s - renew the CA and update channel config (out of scope for reissue)",
			caExpiry.Format(TimeFormat))
	}
	notAfter := caExpiry
	capped := false
	if o.DaysSet {
		requested := now.Add(time.Duration(o.Days) * 24 * time.Hour)
		if requested.After(caExpiry) {
			capped = true
		} else {
			notAfter = requested
		}
	}
	note := ""
	if capped {
		note = " (capped at CA expiry)"
	}
	label := fmt.Sprintf("%s %s", l.Node, l.Kind)
	newExpiry := notAfter.Format(TimeFormat)
	if o.DryRun {
		_, _ = fmt.Fprintf(out, "  DRY-RUN %s -> %s%s\n", label, newExpiry, note)
		return nil
	}
	b, err := Rebuild(l.Cert, ca, caKey, notAfter)
	if err != nil {
		return err
	}
	fi, err := os.Stat(l.CertPath)
	if err != nil {
		return err
	}
	if !o.NoBackup {
		old, err := os.ReadFile(l.CertPath)
		if err != nil {
			return err
		}
		// The path is the leaf's own file, found by Discover in the tree.
		if err := os.WriteFile(BackupPath(l.CertPath), old, fi.Mode().Perm()); err != nil { //nolint:gosec // see above
			return err
		}
	}
	if err := os.WriteFile(l.CertPath, b, fi.Mode().Perm()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "  OK %s -> %s%s\n", label, newExpiry, note)
	return nil
}

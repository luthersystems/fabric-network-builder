package cli

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The command line keeps the Python builder's argparse grammar, because
// substrate, sandbox and ui-core call the builder from Makefiles and scripts
// that this rewrite must not break:
//
//   - Global options come before the command, command options after it.
//   - A long option may be cut to any unique prefix (substrate passes
//     --domain for --domain-name). An exact name wins over a prefix.
//   - --opt=value and --opt value both work.
//   - A list option (argparse nargs='+') takes every following word that
//     does not start with "-".
//
// A general flag library would accept a different grammar, so this small
// parser implements exactly the subset the builder uses.

type kind int

const (
	kindBool   kind = iota // store_true or store_false
	kindString             // one value
	kindInt                // one integer value
	kindList               // nargs='+': one or more values
	kindAppend             // repeatable: one value per use
)

type option struct {
	dest  string
	help  string
	meta  string
	names []string // "--long" and "-s" forms
	kind  kind
	// value is what a bool option stores.
	value bool
}

type positional struct {
	dest string
	help string
}

type command struct {
	name        string
	help        string
	options     []*option
	positionals []positional
}

// values is the result of a parse. A dest is present only when its option
// was given; the caller applies the defaults.
type values struct {
	bools   map[string]bool
	strings map[string]string
	ints    map[string]int
	lists   map[string][]string
}

func newValues() *values {
	return &values{bools: map[string]bool{}, strings: map[string]string{}, ints: map[string]int{}, lists: map[string][]string{}}
}

func (v *values) has(dest string) bool {
	_, b := v.bools[dest]
	_, s := v.strings[dest]
	_, i := v.ints[dest]
	_, l := v.lists[dest]
	return b || s || i || l
}

func (v *values) str(dest, def string) string {
	if s, ok := v.strings[dest]; ok {
		return s
	}
	return def
}

func (v *values) integer(dest string, def int) int {
	if n, ok := v.ints[dest]; ok {
		return n
	}
	return def
}

func (v *values) boolean(dest string, def bool) bool {
	if b, ok := v.bools[dest]; ok {
		return b
	}
	return def
}

// usageError is a command line error. main prints the usage with it and
// exits 2, as argparse does.
type usageError struct {
	cmd *command
	msg string
}

func (e *usageError) Error() string { return e.msg }

func usagef(cmd *command, format string, args ...any) error {
	return &usageError{cmd: cmd, msg: fmt.Sprintf(format, args...)}
}

// errHelp asks main to print the help of cmd (nil: the global help) and
// exit 0.
type errHelp struct{ cmd *command }

func (errHelp) Error() string { return "help requested" }

// lookup finds the option a token names: an exact name, else the one long
// option the token is a prefix of.
func lookup(cmd *command, opts []*option, name string) (*option, error) {
	for _, o := range opts {
		for _, n := range o.names {
			if n == name {
				return o, nil
			}
		}
	}
	if !strings.HasPrefix(name, "--") {
		return nil, usagef(cmd, "unrecognized arguments: %s", name)
	}
	var matches []*option
	var names []string
	for _, o := range opts {
		for _, n := range o.names {
			if strings.HasPrefix(n, "--") && strings.HasPrefix(n, name) {
				matches = append(matches, o)
				names = append(names, n)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, usagef(cmd, "unrecognized arguments: %s", name)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(names)
		return nil, usagef(cmd, "ambiguous option: %s could match %s", name, strings.Join(names, ", "))
	}
}

// negativeNumber matches what argparse reads as a negative number. No
// option of the builder looks like one, so argparse takes such a word as a
// value or a positional (--min-endorsers -1), and so does this parser.
var negativeNumber = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

func isOption(tok string) bool {
	return len(tok) > 1 && strings.HasPrefix(tok, "-") && !negativeNumber.MatchString(tok)
}

// level is one parser level: the global options, or one command's.
type level struct {
	cmd  *command
	opts []*option
	// stopAtPositional stops at the first positional (the command name).
	stopAtPositional bool
}

// readOptions reads the options of one parser level from args into v and
// returns the positionals. With stopAtPositional, the positionals are the
// first one and everything after it.
func (v *values) readOptions(l level, args []string) ([]string, error) {
	cmd, opts := l.cmd, l.opts
	// A bundle of short flags (-fs) is split in place, so work on a copy.
	args = slices.Clone(args)
	var pos []string
	onlyPositionals := false
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if onlyPositionals || !isOption(tok) {
			if l.stopAtPositional {
				return args[i:], nil
			}
			pos = append(pos, tok)
			continue
		}
		if tok == "--" {
			onlyPositionals = true
			continue
		}
		if tok == "-h" || tok == "--help" {
			return nil, errHelp{cmd: cmd}
		}
		name, inline, hasInline := tok, "", false
		if strings.HasPrefix(tok, "--") {
			if k := strings.IndexByte(tok, '='); k >= 0 {
				name, inline, hasInline = tok[:k], tok[k+1:], true
			}
		} else if len(tok) > 2 {
			// -avalue: a short option with its value attached.
			name, inline, hasInline = tok[:2], tok[2:], true
		}
		o, err := lookup(cmd, opts, name)
		if err != nil {
			return nil, err
		}
		display := o.names[0]
		if o.kind == kindBool {
			if hasInline && !strings.HasPrefix(tok, "--") {
				// -fs couchdb: argparse reads -f, then -s couchdb.
				v.bools[o.dest] = o.value
				args[i] = "-" + inline
				i--
				continue
			}
			if hasInline {
				return nil, usagef(cmd, "argument %s: ignored explicit argument %q", display, inline)
			}
			v.bools[o.dest] = o.value
			continue
		}
		var vals []string
		switch {
		case hasInline:
			vals = []string{inline}
		case o.kind == kindList:
			for i+1 < len(args) && !isOption(args[i+1]) {
				i++
				vals = append(vals, args[i])
			}
		case i+1 < len(args) && !isOption(args[i+1]):
			i++
			vals = []string{args[i]}
		}
		if len(vals) == 0 {
			if o.kind == kindList {
				return nil, usagef(cmd, "argument %s: expected at least one argument", display)
			}
			return nil, usagef(cmd, "argument %s: expected one argument", display)
		}
		switch o.kind {
		case kindBool:
			// Handled above: a bool option takes no value.
		case kindString:
			v.strings[o.dest] = vals[0]
		case kindInt:
			n, err := strconv.Atoi(vals[0])
			if err != nil {
				return nil, usagef(cmd, "argument %s: invalid int value: %q", display, vals[0])
			}
			v.ints[o.dest] = n
		case kindList:
			// argparse keeps the last use of an nargs='+' option.
			v.lists[o.dest] = vals
		case kindAppend:
			v.lists[o.dest] = append(v.lists[o.dest], vals[0])
		}
	}
	return pos, nil
}

// invocation is a parsed command line: the command, its positionals by
// dest, and the option values.
type invocation struct {
	cmd *command
	pos map[string]string
	v   *values
}

// parse parses a command line: global options, the command, its options.
func parse(globals []*option, commands []*command, args []string) (*invocation, error) {
	v := newValues()
	rest, err := v.readOptions(level{opts: globals, stopAtPositional: true}, args)
	if err != nil {
		return nil, err
	}
	if len(rest) == 0 {
		return nil, usagef(nil, "the following arguments are required: command")
	}
	var cmd *command
	var names []string
	for _, c := range commands {
		names = append(names, c.name)
		if c.name == rest[0] {
			cmd = c
		}
	}
	if cmd == nil {
		return nil, usagef(nil, "argument command: invalid choice: %q (choose from %s)", rest[0], strings.Join(names, ", "))
	}
	pos, err := v.readOptions(level{cmd: cmd, opts: cmd.options}, rest[1:])
	if err != nil {
		return nil, err
	}
	if len(pos) < len(cmd.positionals) {
		var missing []string
		for _, p := range cmd.positionals[len(pos):] {
			missing = append(missing, p.dest)
		}
		return nil, usagef(cmd, "the following arguments are required: %s", strings.Join(missing, ", "))
	}
	if len(pos) > len(cmd.positionals) {
		return nil, usagef(cmd, "unrecognized arguments: %s", strings.Join(pos[len(cmd.positionals):], " "))
	}
	named := map[string]string{}
	for i, p := range cmd.positionals {
		named[p.dest] = pos[i]
	}
	return &invocation{cmd: cmd, pos: named, v: v}, nil
}

func optionUsage(o *option) string {
	s := strings.Join(o.names, ", ")
	meta := o.meta
	if meta == "" {
		meta = strings.ToUpper(o.dest)
	}
	switch o.kind {
	case kindBool:
	case kindList:
		s += " " + meta + " [" + meta + " ...]"
	default:
		s += " " + meta
	}
	return s
}

func printOptions(w io.Writer, opts []*option) {
	for _, o := range opts {
		_, _ = fmt.Fprintf(w, "  %-34s %s\n", optionUsage(o), o.help)
	}
}

func printUsage(w io.Writer, globals []*option, commands []*command, cmd *command) {
	if cmd == nil {
		_, _ = fmt.Fprintf(w, "usage: %s [global options] command [options]\n\nglobal options:\n", prog)
		printOptions(w, globals)
		_, _ = fmt.Fprintln(w, "\ncommands:")
		for _, c := range commands {
			_, _ = fmt.Fprintf(w, "  %-34s %s\n", c.name, c.help)
		}
		return
	}
	_, _ = fmt.Fprintf(w, "usage: %s [global options] %s [options]", prog, cmd.name)
	for _, p := range cmd.positionals {
		_, _ = fmt.Fprintf(w, " %s", p.dest)
	}
	_, _ = fmt.Fprintf(w, "\n\n%s\n", cmd.help)
	if len(cmd.positionals) > 0 {
		_, _ = fmt.Fprintln(w, "\npositional arguments:")
		for _, p := range cmd.positionals {
			_, _ = fmt.Fprintf(w, "  %-34s %s\n", p.dest, p.help)
		}
	}
	if len(cmd.options) > 0 {
		_, _ = fmt.Fprintln(w, "\noptions:")
		printOptions(w, cmd.options)
	}
}

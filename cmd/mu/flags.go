package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// parseFlagsHook, when non-nil, is called after a successful parseFlags.
// Returning true stops the command (parseFlags reports exitOK, !ok). Tests
// use it to capture a command's parsed flags without running the command.
var parseFlagsHook func(fs *flag.FlagSet) bool

// parseFlags is the flag-parsing entry point for every mu subcommand. It
// installs a usage function showing synopsis ("build [flags] <target>...")
// and the flag defaults, then parses args with parseInterspersed so flags
// may follow positionals. After it returns ok, fs.Args() holds the
// positionals. Help (-h, -help, --help) prints usage to stdout and returns
// (exitOK, false); a parse error has already been reported on fs.Output()
// and returns (exitUsage, false).
func parseFlags(fs *flag.FlagSet, args []string, synopsis string) (int, bool) {
	fs.Usage = func() { printFlagUsage(fs.Output(), fs, synopsis) }
	if _, err := parseInterspersed(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printFlagUsage(os.Stdout, fs, synopsis)
			return exitOK, false
		}
		return exitUsage, false
	}
	if parseFlagsHook != nil && parseFlagsHook(fs) {
		return exitOK, false
	}
	return exitOK, true
}

// printFlagUsage writes "usage: mu <synopsis>" followed by fs's flag
// defaults to w.
func printFlagUsage(w io.Writer, fs *flag.FlagSet, synopsis string) {
	fmt.Fprintf(w, "usage: mu %s\n", synopsis)
	hasFlags := false
	fs.VisitAll(func(*flag.Flag) { hasFlags = true })
	if !hasFlags {
		return
	}
	fmt.Fprintln(w, "\nFlags:")
	out := fs.Output()
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(out)
}

// parseInterspersed parses flags anywhere in args, unlike flag.FlagSet.Parse,
// which stops at the first positional. A bare "--" ends flag parsing; every
// argument after it is positional. It returns the positionals in order and
// also leaves them in fs.Args(). An undefined -h/-help/--help returns
// flag.ErrHelp without printing anything.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positionals = append(positionals, a)
			continue
		}
		name := strings.TrimPrefix(a[1:], "-")
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, hasValue = name[:eq], true
		}
		f := fs.Lookup(name)
		if f == nil && (name == "h" || name == "help") {
			return nil, flag.ErrHelp
		}
		// Hand fs.Parse exactly this flag, plus its value when the value
		// is the next argument, so it reports errors in its usual form.
		n := 1
		if f != nil && !hasValue && !isBoolFlag(f) && i+1 < len(args) {
			n = 2
		}
		if err := fs.Parse(args[i : i+n]); err != nil {
			return nil, err
		}
		i += n - 1
	}
	if err := fs.Parse(append([]string{"--"}, positionals...)); err != nil {
		return nil, err
	}
	return positionals, nil
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// isHelpArg reports whether arg asks a command dispatcher for help.
func isHelpArg(arg string) bool {
	switch arg {
	case "help", "-h", "-help", "--help":
		return true
	}
	return false
}

// dispatcherUsage picks the stream and exit code for a sub-dispatcher's
// command list: stdout and exitOK when help was asked for, stderr and
// exitUsage when the subcommand is missing.
func dispatcherUsage(args []string) (io.Writer, int) {
	if len(args) > 0 && isHelpArg(args[0]) {
		return os.Stdout, exitOK
	}
	return os.Stderr, exitUsage
}

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/chazu/mu/internal/cas/oci"
	"golang.org/x/term"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// runLogin authenticates against an OCI registry and stores the credential
// in ~/.mu/credentials.json (0600). Every registry access mu makes — remote
// cache reads, `mu cache push`, `mu build --publish` — uses that file first.
// mu deliberately does not use the Docker credential chain for writes: that
// chain can require a credential helper binary (e.g.
// `docker-credential-desktop`) which is not installed on most hosts. Reads
// still fall back to the Docker chain, so existing `docker login` /
// `oras login` sessions keep working.
//
// The host is required, except inside a project whose mu.cue names exactly
// one registry host (cache.push, cache.remote, publish.repository).
func runLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("login", fs, useConfig, useOffline)
	username := fs.String("username", "", "registry username (prompted on a TTY when omitted)")
	passwordStdin := fs.Bool("password-stdin", false, "read the password or token from stdin")
	if code, ok := parseFlags(fs, args, "login [flags] <host>"); !ok {
		return code
	}
	if !c.requireRegistry() {
		return exitUsage
	}
	host, code, ok := loginHost(c, fs)
	if !ok {
		return code
	}

	user, pass, code, ok := readCredentials(c, os.Stdin, *username, *passwordStdin)
	if !ok {
		return code
	}

	ctx := c.networkContext(context.Background())
	reg, err := oci.NewRegistry(host)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	store, err := oci.CredentialStore()
	if err != nil {
		return c.fail(exitFail, "open credential store: %v", err)
	}
	if err := credentials.Login(ctx, store, reg, auth.Credential{Username: user, Password: pass}); err != nil {
		return c.fail(exitFail, "login to %s: %v", host, err)
	}

	fmt.Printf("Logged in to %s\n", host)
	return exitOK
}

// runLogout removes the stored credential for a registry host.
func runLogout(args []string) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("logout", fs, useConfig)
	if code, ok := parseFlags(fs, args, "logout [flags] <host>"); !ok {
		return code
	}
	host, code, ok := loginHost(c, fs)
	if !ok {
		return code
	}

	store, err := oci.CredentialStore()
	if err != nil {
		return c.fail(exitFail, "open credential store: %v", err)
	}
	if err := credentials.Logout(context.Background(), store, host); err != nil {
		return c.fail(exitFail, "logout from %s: %v", host, err)
	}

	fmt.Printf("Logged out of %s\n", host)
	return exitOK
}

// runCacheLoginMoved answers the removed `mu cache login` / `cache logout`.
func runCacheLoginMoved(sub string) int {
	fmt.Fprintf(os.Stderr, "mu cache %s: moved to `mu %s <host>`\n", sub, sub)
	return exitUsage
}

// loginHost returns the host positional argument. With none, it loads the
// project (if any) and uses its registry host when mu.cue names exactly one.
// No project is needed when the host is given.
func loginHost(c *cliContext, fs *flag.FlagSet) (string, int, bool) {
	switch fs.NArg() {
	case 0:
	case 1:
		return fs.Arg(0), exitOK, true
	default:
		return "", c.fail(exitUsage, "expected one registry host, got %d arguments", fs.NArg()), false
	}
	if code, ok := c.resolveOptionalProject(); !ok {
		return "", code, false
	}
	hosts := configuredRegistryHosts(c)
	switch len(hosts) {
	case 1:
		return hosts[0], exitOK, true
	case 0:
		return "", c.fail(exitUsage, "no registry host given — usage: mu %s <host>", c.Name), false
	default:
		return "", c.fail(exitUsage, "mu.cue names several registry hosts (%s) — choose one: mu %s <host>",
			strings.Join(hosts, ", "), c.Name), false
	}
}

// configuredRegistryHosts returns the distinct registry hosts mu.cue points
// at (cache.push, cache.remote, publish.repository), sorted.
func configuredRegistryHosts(c *cliContext) []string {
	if c.Config == nil {
		return nil
	}
	var refs []string
	if cc := c.Config.Cache; cc != nil {
		refs = append(refs, cc.Push, cc.Remote)
	}
	if p := c.Config.Publish; p != nil {
		refs = append(refs, p.Repository)
	}
	seen := map[string]bool{}
	var hosts []string
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if h := oci.RegistryHost(ref); !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	sort.Strings(hosts)
	return hosts
}

// readCredentials returns the username and password, reading the password
// from stdin with --password-stdin and prompting on a TTY otherwise. A
// personal access token is passed as the password.
func readCredentials(c *cliContext, stdin *os.File, user string, stdinPass bool) (string, string, int, bool) {
	pass := ""
	if stdinPass {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", "", c.fail(exitFail, "reading password from stdin: %v", err), false
		}
		pass = strings.TrimRight(string(data), "\r\n")
	}

	tty := term.IsTerminal(int(stdin.Fd()))
	if user == "" {
		if !tty {
			return "", "", c.fail(exitUsage, "--username is required when stdin is not a TTY"), false
		}
		fmt.Print("Username: ")
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil {
			return "", "", c.fail(exitFail, "reading username: %v", err), false
		}
		user = strings.TrimRight(line, "\r\n")
	}

	if !stdinPass {
		if !tty {
			return "", "", c.fail(exitUsage, "--password-stdin is required when stdin is not a TTY"), false
		}
		fmt.Print("Password: ")
		b, err := term.ReadPassword(int(stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", "", c.fail(exitFail, "reading password: %v", err), false
		}
		pass = string(b)
	}

	if user == "" {
		return "", "", c.fail(exitUsage, "username must not be empty"), false
	}
	if pass == "" {
		return "", "", c.fail(exitUsage, "password must not be empty"), false
	}
	return user, pass, exitOK, true
}

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// runCacheLogin authenticates against a remote OCI registry and stores the
// resulting credential in ~/.mu/credentials.json. mu deliberately does not
// use the Docker credential chain for writes: that chain can require a
// credential helper binary (e.g. `docker-credential-desktop`) which is not
// installed on most hosts. Reads still fall back to the Docker chain, so
// existing `docker login` / `oras login` sessions keep working.
// The registry defaults to cache.push.registry from mu.json; an explicit
// positional argument overrides it.
func runCacheLogin(args []string) int {
	fs := flag.NewFlagSet("cache login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("cache login", fs)
	username := fs.String("username", "", "registry username")
	password := fs.String("password", "", "registry password (prefer --password-stdin)")
	passwordStdin := fs.Bool("password-stdin", false, "read password from stdin")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if code, ok := c.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}

	registry := fs.Arg(0)
	if registry == "" {
		if c.Config != nil && c.Config.Cache != nil && c.Config.Cache.Push != nil {
			registry = c.Config.Cache.Push.Registry
		}
	}
	if registry == "" {
		return c.fail(exitUsage,
			`no registry given — pass one as an argument (mu cache login <host>) or set cache.push.registry in mu.json`)
	}

	user, pass, code, ok := readCredentials(c, *username, *password, *passwordStdin)
	if !ok {
		return code
	}

	ctx := context.Background()

	reg, err := remote.NewRegistry(registry)
	if err != nil {
		return c.fail(exitFail, "parse registry %q: %v", registry, err)
	}
	if isLocalRegistryHost(registry) {
		reg.PlainHTTP = true
	}

	store, err := openCredentialStore()
	if err != nil {
		return c.fail(exitFail, "open credential store: %v", err)
	}

	cred := auth.Credential{Username: user, Password: pass}
	if user == "" {
		// Treat as identity/refresh token.
		cred = auth.Credential{RefreshToken: pass}
	}

	if err := credentials.Login(ctx, store, reg, cred); err != nil {
		return c.fail(exitFail, "login to %s: %v", registry, err)
	}

	fmt.Printf("Logged in to %s\n", registry)
	return exitOK
}

// runCacheLogout removes any stored credential for the registry.
func runCacheLogout(args []string) int {
	fs := flag.NewFlagSet("cache logout", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("cache logout", fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if code, ok := c.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}

	registry := fs.Arg(0)
	if registry == "" && c.Config != nil && c.Config.Cache != nil && c.Config.Cache.Push != nil {
		registry = c.Config.Cache.Push.Registry
	}
	if registry == "" {
		return c.fail(exitUsage,
			`no registry given — pass one as an argument (mu cache logout <host>) or set cache.push.registry in mu.json`)
	}

	store, err := openCredentialStore()
	if err != nil {
		return c.fail(exitFail, "open credential store: %v", err)
	}
	if err := credentials.Logout(context.Background(), store, registry); err != nil {
		return c.fail(exitFail, "logout from %s: %v", registry, err)
	}

	fmt.Printf("Logged out of %s\n", registry)
	return exitOK
}

func readCredentials(c *cliContext, flagUser, flagPass string, stdinPass bool) (string, string, int, bool) {
	user := flagUser
	pass := flagPass

	if stdinPass {
		if pass != "" {
			return "", "", c.fail(exitUsage, "--password and --password-stdin are mutually exclusive"), false
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", "", c.fail(exitFail, "reading password from stdin: %v", err), false
		}
		pass = strings.TrimRight(string(data), "\r\n")
	}

	if user == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", "", c.fail(exitUsage, "--username is required when stdin is not a TTY"), false
		}
		fmt.Print("Username: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return "", "", c.fail(exitFail, "reading username: %v", err), false
		}
		user = strings.TrimRight(line, "\r\n")
	}

	if pass == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", "", c.fail(exitUsage, "--password or --password-stdin is required when stdin is not a TTY"), false
		}
		fmt.Print("Password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", "", c.fail(exitFail, "reading password: %v", err), false
		}
		pass = string(b)
	}

	if pass == "" {
		return "", "", c.fail(exitUsage, "password must not be empty"), false
	}
	return user, pass, exitOK, true
}


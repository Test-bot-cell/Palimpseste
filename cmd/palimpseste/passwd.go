package main

// palimpseste passwd hashes an admin password for remote edit mode (§14). The
// hash is printed for the operator to place in the environment
// (PALIMPSESTE_ADMIN_HASH) — never in the repository (§3.1). The password is
// read from the terminal without echo; the tool never stores or transmits it.

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"palimpseste/internal/auth"
)

func cmdPasswd(args []string) int {
	fmt.Fprintln(os.Stderr, "Mot de passe administrateur (saisie masquée) :")
	pw, err := readSecret()
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	if strings.TrimSpace(pw) == "" {
		fmt.Fprintln(os.Stderr, "palimpseste: mot de passe vide, abandon")
		return 1
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "\nAjoutez ceci à votre environnement (hors dépôt) :")
	fmt.Printf("export PALIMPSESTE_ADMIN_HASH='%s'\n", hash)
	return 0
}

// readSecret reads one line without echo when stdin is a terminal, falling
// back to a plain line read (piped input) otherwise.
func readSecret() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

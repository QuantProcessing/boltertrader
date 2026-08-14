// Command bootstrap generates the local stack's credentials and the compose
// variables that point at them. `make up` runs it before every
// `docker compose up`, and running it twice is a no-op.
//
// It lives under internal/ rather than cmd/ on purpose: cmd/ is the set of
// production entrypoints the Dockerfile bakes into the image, and a tool whose
// job is to write credentials into the working tree has no business being
// available inside a container.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuantProcessing/boltertrader/internal/devsetup"
)

func main() {
	root := flag.String("root", "", "repository root; defaults to the nearest directory holding go.mod")
	quiet := flag.Bool("quiet", false, "print nothing on success except a newly generated password")
	flag.Parse()

	if err := run(*root, *quiet); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
		os.Exit(1)
	}
}

func run(root string, quiet bool) error {
	if root == "" {
		working, err := os.Getwd()
		if err != nil {
			return err
		}
		if root, err = devsetup.FindRepoRoot(working); err != nil {
			return err
		}
	}

	result, err := devsetup.Bootstrap(root)
	if err != nil {
		return err
	}

	// The password is printed exactly once, on the run that generates it, and
	// it is printed even under -quiet: it is the operator's only copy in a form
	// they can read, since the stack keeps nothing but the bcrypt hash. Every
	// later run points at the file instead.
	if result.Password != "" {
		fmt.Printf("\nBootstrap operator: %s\nBootstrap password: %s\n\nThis is the only time it is printed. It is also in %s (0600).\nTo reset it, delete %s and re-run.\n\n",
			result.Config.BootstrapUsername,
			result.Password,
			relative(result.Root, filepath.Join(result.SecretDir, devsetup.BootstrapPasswordFile)),
			relative(result.Root, filepath.Join(result.SecretDir, devsetup.BootstrapPasswordHashFile)),
		)
	}
	if quiet {
		return nil
	}

	if len(result.Created) > 0 {
		fmt.Printf("generated %s in %s\n", strings.Join(result.Created, ", "), relative(result.Root, result.SecretDir))
	} else {
		fmt.Printf("reused the credentials in %s\n", relative(result.Root, result.SecretDir))
	}
	fmt.Printf("wrote %s and %s\n", relative(result.Root, result.EnvFile), relative(result.Root, result.ServiceFile))
	return nil
}

// relative keeps the output readable when the tool runs from the repository
// root, which is the only place make invokes it from, and falls back to the
// absolute path rather than to something misleading when it does not.
func relative(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

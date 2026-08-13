// Command doctor reports on the local stack and says what to do about anything
// it finds. `make doctor` runs it.
//
// It lives beside bootstrap under internal/ for the same reason: cmd/ is the
// set of entrypoints the Dockerfile bakes into the image, and this tool reads
// the working tree's credential files, which no container should be able to do.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/QuantProcessing/boltertrader/internal/devsetup"
)

func main() {
	// SIGINT-aware, because the postgres and HTTP probes each wait out a
	// timeout and an operator who has seen enough should not have to wait for
	// the rest.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root, err := os.Getwd()
	if err == nil {
		root, err = devsetup.FindRepoRoot(root)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(1)
	}

	checks, err := devsetup.Diagnose(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(1)
	}

	report(checks)
	if devsetup.Failed(checks) {
		os.Exit(1)
	}
}

// report prints one line per check, then the fixes. Splitting them keeps the
// summary scannable when nothing is wrong — which is the common case, and the
// one where a wall of remediation advice is pure noise.
func report(checks []devsetup.Check) {
	width := 0
	for _, check := range checks {
		if len(check.Name) > width {
			width = len(check.Name)
		}
	}

	var fixes []string
	for _, check := range checks {
		fmt.Printf("%s  %-*s  %s\n", marker(check.Status), width, check.Name, check.Detail)
		if check.Status == devsetup.Fail && check.Fix != "" {
			fixes = append(fixes, "  "+check.Name+": "+check.Fix)
		}
	}
	if len(fixes) > 0 {
		fmt.Printf("\nto fix:\n%s\n", strings.Join(fixes, "\n"))
	}
}

func marker(status devsetup.Status) string {
	switch status {
	case devsetup.Pass:
		return "ok  "
	case devsetup.Fail:
		return "FAIL"
	default:
		return "skip"
	}
}

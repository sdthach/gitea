// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Command gitea-delivery is a thin client over /api/delivery/v1. It holds no business
// logic, reaches no database, and knows no rule the API does not enforce: flags become query
// parameters and the server decides.
//
// Gitea's own tea CLI is a separate project, absent from go.mod and the Makefile, and models
// no environment or deployment; it is not forked.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "gitea-delivery: %s\n", err.Message)
		fmt.Fprintf(os.Stderr, "  Suggested action: %s\n", err.SuggestedAction)
		os.Exit(err.ExitCode)
	}
}

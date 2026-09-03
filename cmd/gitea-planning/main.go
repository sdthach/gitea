// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Command gitea-planning is a thin client over /api/planning/v1. It holds no business
// logic, reaches no database, and knows no rule the API does not enforce: flags become query
// parameters and the server decides.
//
// Gitea's own tea CLI is a separate project, absent from go.mod and the Makefile, and models
// no project or roadmap; it is not forked.
package main

import (
	"fmt"
	"os"

	"gitea.dev/cmd/hubcli"
)

var config = hubcli.Config{
	Name:          "gitea-planning",
	BasePath:      "/api/planning/v1",
	DocPath:       "docs/planning/openapi.json",
	Commands:      Commands,
	TokenEnvVars:  []string{"GITEA_PLANNING_TOKEN", "FORGE_TOKEN", "GITEA_TOKEN"},
	ServerEnvVars: []string{"GITEA_PLANNING_SERVER", "GITEA_SERVER", "FORGE_HOST"},
}

func main() {
	if err := hubcli.Run(config, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "gitea-planning: %s\n", err.Message)
		fmt.Fprintf(os.Stderr, "  Suggested action: %s\n", err.SuggestedAction)
		os.Exit(err.ExitCode)
	}
}

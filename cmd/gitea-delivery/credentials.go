// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"strings"
)

// tokenSources is the credential precedence, matching what ccpm's detect.sh resolves, so
// one token serves the adapter and the CLI. The CLI stores nothing new (K8, B4).
var tokenSources = []string{"GITEA_DELIVERY_TOKEN", "FORGE_TOKEN", "GITEA_TOKEN"}

// serverSources is the same idea for the base URL.
var serverSources = []string{"GITEA_DELIVERY_SERVER", "GITEA_SERVER", "FORGE_HOST"}

// ResolveToken applies the precedence to an explicit flag and an environment lookup. It is
// pure over its inputs so every branch is testable without touching the process env.
func ResolveToken(flagValue string, env func(string) (string, bool)) (string, string) {
	if flagValue != "" {
		return flagValue, "--token"
	}
	for _, name := range tokenSources {
		if v, ok := env(name); ok && v != "" {
			return v, "$" + name
		}
	}
	return "", ""
}

// ResolveServer applies the same precedence to the base URL.
func ResolveServer(flagValue string, env func(string) (string, bool)) (string, string) {
	if flagValue != "" {
		return strings.TrimRight(flagValue, "/"), "--server"
	}
	for _, name := range serverSources {
		if v, ok := env(name); ok && v != "" {
			return strings.TrimRight(v, "/"), "$" + name
		}
	}
	return "", ""
}

func resolveServer(flagValue string) (string, *Error) {
	value, _ := ResolveServer(flagValue, os.LookupEnv)
	if value == "" {
		return "", failf(2,
			"Pass --server https://gitea.example.com, or set "+strings.Join(serverSources, " or ")+".",
			"no Gitea server was resolved")
	}
	return value, nil
}

func resolveToken(flagValue, server string) (string, *Error) {
	value, _ := ResolveToken(flagValue, os.LookupEnv)
	if value == "" {
		return "", failf(2,
			"Create an API token in Gitea at "+server+"/user/settings/applications and pass it with --token, or set "+strings.Join(tokenSources, " or ")+".",
			"no API token was resolved")
	}
	return value, nil
}

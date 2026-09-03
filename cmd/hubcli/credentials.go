// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hubcli

import (
	"os"
	"strings"
)

// ResolveToken applies cfg's token precedence to an explicit flag and an environment lookup.
// It is pure over its inputs so every branch is testable without touching the process env.
func ResolveToken(flagValue string, envVars []string, env func(string) (string, bool)) (string, string) {
	if flagValue != "" {
		return flagValue, "--token"
	}
	for _, name := range envVars {
		if v, ok := env(name); ok && v != "" {
			return v, "$" + name
		}
	}
	return "", ""
}

// ResolveServer applies the same precedence to the base URL.
func ResolveServer(flagValue string, envVars []string, env func(string) (string, bool)) (string, string) {
	if flagValue != "" {
		return strings.TrimRight(flagValue, "/"), "--server"
	}
	for _, name := range envVars {
		if v, ok := env(name); ok && v != "" {
			return strings.TrimRight(v, "/"), "$" + name
		}
	}
	return "", ""
}

func resolveServer(cfg Config, flagValue string) (string, *Error) {
	value, _ := ResolveServer(flagValue, cfg.ServerEnvVars, os.LookupEnv)
	if value == "" {
		return "", failf(2,
			"Pass --server https://gitea.example.com, or set "+strings.Join(cfg.ServerEnvVars, " or ")+".",
			"no Gitea server was resolved")
	}
	return value, nil
}

func resolveToken(cfg Config, flagValue, server string) (string, *Error) {
	value, _ := ResolveToken(flagValue, cfg.TokenEnvVars, os.LookupEnv)
	if value == "" {
		return "", failf(2,
			"Create an API token in Gitea at "+server+"/user/settings/applications and pass it with --token, or set "+strings.Join(cfg.TokenEnvVars, " or ")+".",
			"no API token was resolved")
	}
	return value, nil
}

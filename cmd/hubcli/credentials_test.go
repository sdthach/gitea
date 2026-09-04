// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hubcli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCredentialPrecedence: one token serves the adapter and the CLI.
func TestCredentialPrecedence(t *testing.T) {
	env := func(pairs map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) { v, ok := pairs[name]; return v, ok }
	}
	envVars := []string{"GITEA_HUBCLI_TOKEN", "FORGE_TOKEN", "GITEA_TOKEN"}
	cases := []struct {
		name       string
		flag       string
		env        map[string]string
		wantValue  string
		wantSource string
	}{
		{"flag wins", "flagged", map[string]string{"GITEA_HUBCLI_TOKEN": "a"}, "flagged", "--token"},
		{"binary variable", "", map[string]string{"GITEA_HUBCLI_TOKEN": "a", "FORGE_TOKEN": "b"}, "a", "$GITEA_HUBCLI_TOKEN"},
		{"forge variable", "", map[string]string{"FORGE_TOKEN": "b", "GITEA_TOKEN": "c"}, "b", "$FORGE_TOKEN"},
		{"gitea variable", "", map[string]string{"GITEA_TOKEN": "c"}, "c", "$GITEA_TOKEN"},
		{"empty is not set", "", map[string]string{"GITEA_HUBCLI_TOKEN": "", "GITEA_TOKEN": "c"}, "c", "$GITEA_TOKEN"},
		{"nothing", "", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			value, source := ResolveToken(c.flag, envVars, env(c.env))
			assert.Equal(t, c.wantValue, value)
			assert.Equal(t, c.wantSource, source, "the CLI must be able to report which credential source won")
		})
	}
}

func TestResolveServerTrimsTrailingSlash(t *testing.T) {
	value, source := ResolveServer("https://gitea.example.invalid/", []string{"GITEA_HUBCLI_SERVER"}, func(string) (string, bool) { return "", false })
	assert.Equal(t, "https://gitea.example.invalid", value)
	assert.Equal(t, "--server", source)
}

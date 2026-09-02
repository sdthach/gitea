// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestBody reads back what the CLI composed. http.NewRequest over a bytes.Reader sets
// GetBody, so the recorder can be asked for the payload after the fact.
func requestBody(t *testing.T, req *http.Request) string {
	t.Helper()
	if req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	require.NoError(t, err)
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(raw)
}

// TestDeliveryDeployAndRollbackComposeTheIdenticalRequest: rolling back is deploying a
// prior release tag, so the two commands compose the same request and differ only in the tag. A second dispatch path would fail this.
func TestDeliveryDeployAndRollbackComposeTheIdenticalRequest(t *testing.T) {
	args := func(command, tag string) []string {
		return []string{command, "--repo", "acme/web", "--environment", "prod", "--release-tag", tag, "--confirm"}
	}

	rec := &recorder{body: "{}", status: http.StatusCreated}
	withRecorder(t, rec)
	_, _, err := exec(t, rec, args("deploy", "v2.0")...)
	require.Nil(t, err)

	rollback := &recorder{body: "{}", status: http.StatusCreated}
	withRecorder(t, rollback)
	_, _, err = exec(t, rollback, args("rollback", "v2.0")...)
	require.Nil(t, err)

	require.Len(t, rec.requests, 1)
	require.Len(t, rollback.requests, 1)
	deployReq, rollbackReq := rec.requests[0], rollback.requests[0]

	assert.Equal(t, http.MethodPost, deployReq.Method)
	assert.Equal(t, deployReq.Method, rollbackReq.Method)
	assert.Equal(t, "/api/delivery/v1/deployments", deployReq.URL.Path)
	assert.Equal(t, deployReq.URL.String(), rollbackReq.URL.String())
	assert.Equal(t, requestBody(t, deployReq), requestBody(t, rollbackReq),
		"at the same release tag the two commands are byte-identical; only the tag ever differs")

	// And with a prior tag, the ONLY difference is the tag.
	earlier := &recorder{body: "{}", status: http.StatusCreated}
	withRecorder(t, earlier)
	_, _, err = exec(t, earlier, args("rollback", "v1.0")...)
	require.Nil(t, err)
	assert.NotEqual(t, requestBody(t, deployReq), requestBody(t, earlier.requests[0]))
	assert.JSONEq(t,
		`{"confirm":true,"environment":"prod","release_tag":"v2.0","repo":"acme/web"}`,
		requestBody(t, deployReq))
	assert.JSONEq(t,
		`{"confirm":true,"environment":"prod","release_tag":"v1.0","repo":"acme/web"}`,
		requestBody(t, earlier.requests[0]))

	// The bytes are deterministic, not merely equivalent: members are written in the order
	// the generated command lists them, which is sorted. Without that, "identical request"
	// would be a claim about two encodings rather than about two byte strings.
	assert.True(t, strings.HasPrefix(requestBody(t, deployReq), `{"confirm":`),
		"body members are written in sorted order")
}

// TestDeliveryDeployWithoutConfirmSendsTheFirstStep covers the CLI's half of the two-step
// deploy: the default is a plan, and nothing is dispatched until --confirm is given. It is the server's
// rule, so the CLI expresses it rather than enforcing a second copy of it.
func TestDeliveryDeployWithoutConfirmSendsTheFirstStep(t *testing.T) {
	rec := &recorder{body: `{"outcome":"warn","confirmed":false}`}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "deploy", "--repo", "acme/web", "--environment", "prod", "--release-tag", "v2.0")
	require.Nil(t, err)
	require.Len(t, rec.requests, 1)
	assert.JSONEq(t,
		`{"environment":"prod","release_tag":"v2.0","repo":"acme/web"}`,
		requestBody(t, rec.requests[0]),
		"an unset switch is omitted, so the endpoint's own default — plan, do not dispatch — stands")
}

// TestDeliveryOverrideReasonIsSentVerbatim: the reason reaches the API, which is what puts
// it on the audit log. The CLI holds no copy of the rule that demands it.
func TestDeliveryOverrideReasonIsSentVerbatim(t *testing.T) {
	rec := &recorder{body: "{}", status: http.StatusCreated}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "deploy", "--repo", "acme/web", "--environment", "prod",
		"--release-tag", "v2.0", "--confirm", "--override-reason", "hotfix; staging is down")
	require.Nil(t, err)
	assert.JSONEq(t,
		`{"confirm":true,"environment":"prod","override_reason":"hotfix; staging is down","release_tag":"v2.0","repo":"acme/web"}`,
		requestBody(t, rec.requests[0]))
}

// TestDeliveryDeployNamesItsMissingArguments: a request the endpoint would refuse is refused
// before the round-trip, naming what is missing and how to call it.
func TestDeliveryDeployNamesItsMissingArguments(t *testing.T) {
	rec := &recorder{body: "{}"}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "deploy", "--repo", "acme/web")
	require.NotNil(t, err)
	assert.Equal(t, 2, err.ExitCode)
	assert.Contains(t, err.Message, "--environment")
	assert.Contains(t, err.Message, "--release-tag")
	assert.NotEmpty(t, err.SuggestedAction)
	assert.Empty(t, rec.requests, "nothing was sent")
}

// TestDeliveryCLIRefusalCarriesTheAPIsOwnAction: a deploy the API refuses is
// refused at the CLI with the API's own words, and the CLI applies no rule of its own.
func TestDeliveryCLIRefusalCarriesTheAPIsOwnAction(t *testing.T) {
	rec := &recorder{
		status: http.StatusForbidden,
		body: `{"message":"prod requires its predecessor staging to have held this release, and it never has",` +
			`"suggested_action":"Deploy the release to staging first, or ask someone on prod's bypass allowlist to override with a reason."}`,
	}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "deploy", "--repo", "acme/web", "--environment", "prod",
		"--release-tag", "v2.0", "--confirm")
	require.NotNil(t, err)
	assert.Equal(t, 3, err.ExitCode, "refused by the server")
	assert.Contains(t, err.Message, "requires its predecessor staging")
	assert.Contains(t, err.SuggestedAction, "bypass allowlist")
}

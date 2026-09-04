// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gsession "gitea.dev/modules/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The API and web routers each called MustInitSessioner independently, so with the default
// memory session provider they got two session managers with two separate stores: a value set
// through one manager's request was invisible when read back through the other's. Sharing one
// manager, memoized behind sync.OnceValue, is what makes a cookie issued by either readable by
// both; reverting to a plain constructor turns this red (reflect pointer equality on the
// returned middleware does not, since Go gives every closure instantiated from the same
// literal the same code pointer regardless of captured state).
func TestMustInitSessionerSharesOneManager(t *testing.T) {
	mwA := MustInitSessioner()
	mwB := MustInitSessioner()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mwA(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		assert.NoError(t, gsession.GetContextSession(r).Set("probe", "set-by-a"))
	})).ServeHTTP(rec, req)

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1, "the sessioner should have issued exactly one cookie")

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookies[0])
	var got any
	mwB(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = gsession.GetContextSession(r).Get("probe")
	})).ServeHTTP(rec2, req2)

	assert.Equal(t, "set-by-a", got, "a value set through one caller's manager must be readable through the other's")
}

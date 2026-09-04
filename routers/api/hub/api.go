// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package hub is what every fork API area shares: the endpoint/Operation/Param contract,
// authentication, rendering and the OpenAPI document builder. routers/api/planning/v1 and
// routers/api/deployments/v1 mount their own namespace from these types; neither depends on
// the other.
//
// Every endpoint is an Operation before it is a handler: an area's Routes call mounts its own
// endpoint list, and its published OpenAPI document is generated from the same list. An
// endpoint that is not in the document cannot be served, and a documented endpoint with no
// handler is fatal.
package hub

import (
	"net/http"
	"sort"
	"strings"

	"gitea.dev/modules/log"
	"gitea.dev/modules/session"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/util"
	"gitea.dev/modules/web"
	"gitea.dev/routers/common"
	auth_service "gitea.dev/services/auth"
	"gitea.dev/services/context"

	chi_middleware "github.com/go-chi/chi/v5/middleware"
)

// Endpoint binds one documented Operation to its handler and its authorization.
type Endpoint struct {
	Op      *Operation
	Middle  []func(*context.APIContext)
	Handler func(*context.APIContext)
}

// OperationsFrom sorts an area's endpoint list into its documented operation set, so the
// generated document is byte-stable across runs.
func OperationsFrom(eps []*Endpoint) []*Operation {
	ops := make([]*Operation, 0, len(eps))
	for _, e := range eps {
		ops = append(ops, e.Op)
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	return ops
}

func buildAuthGroup() *auth_service.Group {
	group := auth_service.NewGroup(&auth_service.OAuth2{}, &auth_service.HTTPSign{}, &auth_service.Basic{}, &auth_service.Session{})
	if setting.Service.EnableReverseProxyAuthAPI {
		group.Add(&auth_service.ReverseProxy{})
	}
	return group
}

// apiAuth authenticates as the calling user. Every endpoint then authorizes through Gitea's
// own permission check; the API grants nothing the UI does not.
func apiAuth(authMethod auth_service.Method) func(*context.APIContext) {
	return func(ctx *context.APIContext) {
		var sessionStore auth_service.SessionStore
		if ctx.Req.Method == http.MethodGet || ctx.Req.Method == http.MethodHead {
			// The middleware attaches the session to the request it hands its next
			// handler, not to ctx.Req itself, so that request has to be captured back out.
			common.MustInitSessioner()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx.Req = r })).ServeHTTP(ctx.Resp, ctx.Req)
			sessionStore = session.GetContextSession(ctx.Req)
		}
		ar, err := common.AuthShared(ctx.Base, sessionStore, authMethod)
		if err != nil {
			msg, ok := auth_service.ErrAsUserAuthMessage(err)
			msg = util.Iif(ok, msg, "invalid username, password or token")
			APIError(ctx, http.StatusUnauthorized, "unauthenticated", msg,
				"Send an API token in the Authorization header: `Authorization: token <your token>`.")
			return
		}
		ctx.Doer = ar.Doer
		ctx.IsSigned = ar.Doer != nil
		ctx.IsBasicAuth = ar.IsBasicAuth
	}
}

func reqSignIn(ctx *context.APIContext) {
	if !ctx.IsSigned {
		APIError(ctx, http.StatusForbidden, "sign_in_required",
			"only a signed-in user may call this API",
			"Create an API token in Gitea under Settings > Applications and send it in the Authorization header.")
	}
}

// Routes builds one area's namespace, mounted at basePath from routers/init.go.
func Routes(basePath string, eps []*Endpoint) *web.Router {
	m := web.NewRouter()
	m.BeforeRouting(chi_middleware.GetHead)
	m.AfterRouting(context.APIContexter())
	m.AfterRouting(apiAuth(buildAuthGroup()))
	m.AfterRouting(reqSignIn)

	for _, e := range eps {
		if e.Handler == nil {
			// A documented operation with no handler is a defect, not a 404 to discover
			// in production.
			log.Fatal("%s operation %q has no handler", basePath, e.Op.ID)
		}
		handlers := make([]any, 0, len(e.Middle)+1)
		for _, mw := range e.Middle {
			handlers = append(handlers, mw)
		}
		handlers = append(handlers, e.Handler)
		switch strings.ToUpper(e.Op.Method) {
		case http.MethodGet:
			m.Get(e.Op.Path, handlers...)
		case http.MethodPost:
			m.Post(e.Op.Path, handlers...)
		case http.MethodPut:
			m.Put(e.Op.Path, handlers...)
		case http.MethodPatch:
			m.Patch(e.Op.Path, handlers...)
		case http.MethodDelete:
			m.Delete(e.Op.Path, handlers...)
		default:
			log.Fatal("%s operation %q uses unsupported method %q", basePath, e.Op.ID, e.Op.Method)
		}
	}

	m.NotFound(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, `{"code":"not_found","message":"no such endpoint","suggested_action":"Fetch `+basePath+`/../openapi.json to see the endpoints this build serves."}`, http.StatusNotFound)
	})
	return m
}

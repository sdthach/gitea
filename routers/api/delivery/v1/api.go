// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package v1 is the fork's own API namespace, mounted at /api/delivery/v1 from
// routers/init.go rather than as a group inside routers/api/v1/api.go.
//
// Every endpoint is an Operation before it is a handler: Routes mounts the endpoint list,
// and the published OpenAPI document is generated from the same list. An endpoint that is
// not in the document cannot be served, and a documented endpoint with no handler is fatal.
package v1

import (
	"net/http"
	"sort"
	"strings"
	"sync"

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

var sessioner = sync.OnceValue(common.MustInitSessioner)

// endpoint binds one documented Operation to its handler and its authorization.
type endpoint struct {
	Op      *Operation
	Middle  []func(*context.APIContext)
	Handler func(*context.APIContext)
}

// endpoints is the single list. Adding a handler without an Operation here does not make it
// reachable, which is what keeps the document and the implementation from drifting.
func endpoints() []*endpoint {
	return []*endpoint{
		listEnvironmentsEndpoint(),
		listRepoEnvironmentsEndpoint(),
		getRepoEnvironmentEndpoint(),
		listRepoEnvironmentSecretsEndpoint(),
		listReposEndpoint(),
		listDeploymentsEndpoint(),
		listAuditEndpoint(),
		listReleasesEndpoint(),
		getGridEndpoint(),
		createDeploymentEndpoint(),    // slice 5
		listRunsEndpoint(),            // slice 8
		listWorkflowsEndpoint(),       // slice 8
		getOverviewEndpoint(),         // slice 8
		getOverviewTrendsEndpoint(),   // slice 8
		listOverviewReposEndpoint(),   // slice 8
		listApprovalsEndpoint(),       // slice 6
		approveEndpoint(),             // slice 6
		rejectEndpoint(),              // slice 6
		getBoardEndpoint(),            // slice 7
		moveBoardCardColumnEndpoint(), // slice 7
		moveBoardCardLaneEndpoint(),   // slice 7
		getTimelineEndpoint(),         // slice 7
	}
}

// Operations returns the documented operation set, sorted so the generated document is
// byte-stable across runs.
func Operations() []*Operation {
	eps := endpoints()
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
	group := auth_service.NewGroup(&auth_service.OAuth2{}, &auth_service.HTTPSign{}, &auth_service.Basic{})
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
			sessioner()(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})).ServeHTTP(ctx.Resp, ctx.Req)
			sessionStore = session.GetContextSession(ctx.Req)
		}
		ar, err := common.AuthShared(ctx.Base, sessionStore, authMethod)
		if err != nil {
			msg, ok := auth_service.ErrAsUserAuthMessage(err)
			msg = util.Iif(ok, msg, "invalid username, password or token")
			apiError(ctx, http.StatusUnauthorized, "unauthenticated", msg,
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
		apiError(ctx, http.StatusForbidden, "sign_in_required",
			"only a signed-in user may call the delivery API",
			"Create an API token in Gitea under Settings > Applications and send it in the Authorization header.")
	}
}

// Routes builds the namespace. It is mounted from routers/init.go.
func Routes() *web.Router {
	m := web.NewRouter()
	m.BeforeRouting(chi_middleware.GetHead)
	m.AfterRouting(context.APIContexter())
	m.AfterRouting(apiAuth(buildAuthGroup()))
	m.AfterRouting(reqSignIn)

	for _, e := range endpoints() {
		if e.Handler == nil {
			// A documented operation with no handler is a defect, not a 404 to discover
			// in production.
			log.Fatal("delivery API operation %q has no handler", e.Op.ID)
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
		default:
			log.Fatal("delivery API operation %q uses unsupported method %q", e.Op.ID, e.Op.Method)
		}
	}

	m.NotFound(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, `{"code":"not_found","message":"no such delivery endpoint","suggested_action":"Fetch `+BasePath+`/../openapi.json, or run gitea-delivery --help, to see the endpoints this build serves."}`, http.StatusNotFound)
	})
	return m
}

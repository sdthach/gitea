// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package v1 is the planning area of the fork's own API namespace, mounted at
// /api/planning/v1 from routers/init.go rather than as a group inside routers/api/v1/api.go.
// Its endpoint/Operation/Param contract, authentication, rendering and OpenAPI builder live in
// routers/api/hub, shared with routers/api/deployments/v1.
package v1

import (
	"gitea.dev/modules/web"
	hubapi "gitea.dev/routers/api/hub"
)

// BasePath is where the namespace mounts.
const BasePath = "/api/planning/v1"

// endpoints is the single list. Adding a handler without an Operation here does not make it
// reachable, which is what keeps the document and the implementation from drifting.
func endpoints() []*hubapi.Endpoint {
	return []*hubapi.Endpoint{
		getBoardEndpoint(),
		moveBoardCardColumnEndpoint(),
		moveBoardCardGroupEndpoint(),
		getRoadmapEndpoint(),
		moveIssueMilestoneEndpoint(),
		moveIssueGroupEndpoint(),
		setIssueDatesEndpoint(),
		createMilestoneEndpoint(),
		createIssueEndpoint(),
		issueEndpoint(),
		setIssueScheduleEndpoint(),
		clearIssueScheduleEndpoint(),
		setMilestoneScheduleEndpoint(),
		clearMilestoneScheduleEndpoint(),
		setIssueEstimateEndpoint(),
		setIssueParentEndpoint(),
		clearIssueParentEndpoint(),
		getIssueTypesEndpoint(),
		createIssueTypeEndpoint(),
		updateIssueTypeEndpoint(),
		deleteIssueTypeEndpoint(),
		setIssueTypeEndpoint(),
		clearIssueTypeEndpoint(),
		getIssueTypeAssignmentsEndpoint(),
	}
}

// Endpoints exposes the area's endpoint list to the generator.
func Endpoints() []*hubapi.Endpoint { return endpoints() }

// Operations returns the documented operation set, sorted so the generated document is
// byte-stable across runs.
func Operations() []*hubapi.Operation { return hubapi.OperationsFrom(endpoints()) }

// Routes builds the namespace. It is mounted from routers/init.go.
func Routes() *web.Router { return hubapi.Routes(BasePath, endpoints()) }

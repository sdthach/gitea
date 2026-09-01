// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/delivery"
)

const (
	tplBoard    templates.TplName = "delivery/board"
	tplTimeline templates.TplName = "delivery/timeline"
)

// Board renders /delivery/board: Gitea's project columns vertically, with the horizontal
// lanes Gitea does not model (O1).
//
// Like every other fork page it is a client of a documented endpoint and reads nothing the
// API does not serve (E18, I14): the handler passes the API base and the grouping vocabulary
// and nothing else. Lane assignment, the empty-value lane and both writes are the server's,
// so the page and the CLI cannot disagree about which lane a card is in.
func Board(ctx *context.Context) {
	ctx.Data["Title"] = "Board"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.Data["Groupings"] = delivery_service.Groupings
	ctx.HTML(http.StatusOK, tplBoard)
}

// Timeline renders /delivery/timeline: one bar per issue with dependency arrows (O6).
//
// It needs no Projects API, so it renders from the same dataset on a build whose board
// degrades (SC 38). Every bar names the source of each of its endpoints, and an inferred end
// is drawn distinctly from a recorded one (O8) — the distinction is the point of the view,
// so the page never renders a bar without it.
func Timeline(ctx *context.Context) {
	ctx.Data["Title"] = "Delivery timeline"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.HTML(http.StatusOK, tplTimeline)
}

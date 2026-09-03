// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/planning"
)

const (
	tplBoard    templates.TplName = "delivery/board"
	tplTimeline templates.TplName = "delivery/timeline"
)

// Board renders /delivery/board: Gitea's project columns vertically, with horizontal lanes
// Gitea does not model. Lane assignment, the empty-value lane and both writes are the
// server's, so the page and the CLI cannot disagree about which lane a card is in.
func Board(ctx *context.Context) {
	ctx.Data["Title"] = "Board"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.Data["Groupings"] = delivery_service.Groupings
	setPageToken(ctx)
	ctx.HTML(http.StatusOK, tplBoard)
}

// Timeline renders /delivery/timeline: one bar per issue with dependency arrows. It needs
// no Projects API, so it renders on a build whose board degrades. Every bar names the source
// of each endpoint, and an inferred end is drawn distinctly from a recorded one.
func Timeline(ctx *context.Context) {
	ctx.Data["Title"] = "Delivery timeline"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	setPageToken(ctx)
	ctx.HTML(http.StatusOK, tplTimeline)
}

// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/services/context"
)

const tplPromote templates.TplName = "delivery/promote"

// Promote renders /delivery/promote. It is the confirm step: the page POSTs to /deployments
// with confirm=false first, then confirm=true when the human presses the button. The
// sequence rule is applied by the API, so the CLI is refused exactly where the page is.
func Promote(ctx *context.Context) {
	ctx.Data["Title"] = "Confirm deploy"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	setPageToken(ctx)
	ctx.HTML(http.StatusOK, tplPromote)
}

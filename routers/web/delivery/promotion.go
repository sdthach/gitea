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

// Promote renders /delivery/promote, the second of E14's two steps.
//
// It is the confirm step and nothing else: the handler serves the page shell, and the page
// asks POST /deployments with confirm false for the target environment, the release tag, the
// release currently live there and what the sequence rule decided — then sends the same
// request again with confirm true only when the human presses the button. Every figure on it
// arrives over the public API (E18, I14), and the rule is applied by the API rather than by
// this page, so the CLI is refused exactly where the page is (K6).
func Promote(ctx *context.Context) {
	ctx.Data["Title"] = "Confirm deploy"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.HTML(http.StatusOK, tplPromote)
}

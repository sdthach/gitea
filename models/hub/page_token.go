// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"time"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
)

// PageTokenName is the access token name minted for a fork page's own API calls.
const PageTokenName = "_hub_page"

// SweepPageTokens deletes page tokens minted before olderThan, so a stale one outlives at
// most one sweep interval rather than sitting on the account indefinitely.
func SweepPageTokens(ctx context.Context, olderThan time.Time) (int64, error) {
	return db.GetEngine(ctx).
		Where("name = ? AND created_unix < ?", PageTokenName, timeutil.TimeStamp(olderThan.Unix())).
		Delete(new(auth_model.AccessToken))
}

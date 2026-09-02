// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package query

import (
	"testing"

	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

func condSQL(t *testing.T, cond builder.Cond) string {
	t.Helper()
	if !cond.IsValid() {
		return ""
	}
	sql, _, err := builder.ToSQL(cond)
	require.NoError(t, err)
	return sql
}

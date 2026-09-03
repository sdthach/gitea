// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	"gitea.dev/models/unittest"
)

// TestMain runs the package's database-backed units on SQLite, Gitea's own default for
// test-backend.
func TestMain(m *testing.M) {
	unittest.MainTest(m)
}

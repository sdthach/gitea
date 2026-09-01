// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import "xorm.io/builder"

func builderEq(column string, value any) builder.Cond { return builder.Eq{column: value} }

// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
)

// TestPlanningIssueSidebarOnlyOnIssues: templates/repo/issue/view_content/sidebar.tmpl's own
// spoke line renders templates/planning/issue_sidebar.tmpl unconditionally, so the fragment
// itself is what gates on Issue.IsPull — this proves the gate actually holds, on both sides.
func TestPlanningIssueSidebarOnlyOnIssues(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1") // issue 1: an issue, not a pull
	htmlDoc := NewHTMLParser(t, MakeRequest(t, req, http.StatusOK).Body)
	assert.Equal(t, 1, htmlDoc.doc.Find(".issue-sidebar-planning").Length(),
		"an issue's own sidebar carries the planning fragment")

	req = NewRequest(t, "GET", "/user2/repo1/pulls/2") // issue 2 is a pull, at its own /pulls/ URL
	htmlDoc = NewHTMLParser(t, MakeRequest(t, req, http.StatusOK).Body)
	assert.Equal(t, 0, htmlDoc.doc.Find(".issue-sidebar-planning").Length(),
		"a pull's sidebar shares the same template but must not carry the planning fragment")
}

// TestPlanningTypeIconOnIssueList: templates/shared/issuelist.tmpl's own spoke line renders
// templates/planning/issue_type_icon.tmpl once per row, beside shared/issueicon.
func TestPlanningTypeIconOnIssueList(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues")
	htmlDoc := NewHTMLParser(t, MakeRequest(t, req, http.StatusOK).Body)
	assert.Positive(t, htmlDoc.doc.Find(".planning-type-icon").Length(),
		"every row on the issue list carries a type-icon mount")
}

// TestPlanningMilestoneStartOnEditFormOnly: templates/repo/issue/milestone_new.tmpl's own spoke
// line renders templates/planning/milestone_start.tmpl, which itself gates on
// PageIsEditMilestone — new-milestone shares the same upstream template and must not carry it.
func TestPlanningMilestoneStartOnEditFormOnly(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")

	req := NewRequest(t, "GET", "/user2/repo1/milestones/1/edit")
	htmlDoc := NewHTMLParser(t, session.MakeRequest(t, req, http.StatusOK).Body)
	assert.Equal(t, 1, htmlDoc.doc.Find("[data-global-init=\"initPlanningMilestoneStart\"]").Length(),
		"the edit form carries the milestone-start mount")

	req = NewRequest(t, "GET", "/user2/repo1/milestones/new")
	htmlDoc = NewHTMLParser(t, session.MakeRequest(t, req, http.StatusOK).Body)
	assert.Equal(t, 0, htmlDoc.doc.Find("[data-global-init=\"initPlanningMilestoneStart\"]").Length(),
		"the new-milestone form shares the same upstream template and must not carry the mount")
}

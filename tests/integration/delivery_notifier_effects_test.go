// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/url"
	"testing"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/db"
	git_model "gitea.dev/models/git"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/commitstatus"
	"gitea.dev/modules/git"
	"gitea.dev/modules/timeutil"
	"gitea.dev/services/notify"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployAtSHA drives one deploy through Gitea's own notifier at a real commit, which is what
// the commit status and the tag both need: a SHA the repository actually holds.
func deployAtSHA(t *testing.T, repo *repo_model.Repository, sender *user_model.User, environment, tag, sha string, runID int64, status actions_model.Status) {
	t.Helper()
	require.NoError(t, repo.LoadOwner(t.Context()))

	run := &actions_model.ActionRun{
		ID: runID, RepoID: repo.ID, WorkflowID: "deploy-" + environment + ".yaml",
		Ref: "refs/tags/" + tag, CommitSHA: sha,
		Status: actions_model.StatusWaiting, Updated: timeutil.TimeStamp(1000),
	}
	notify.WorkflowRunStatusUpdate(t.Context(), repo, sender, run)

	run.Status = status
	run.Updated = timeutil.TimeStamp(1010)
	notify.WorkflowRunStatusUpdate(t.Context(), repo, sender, run)
}

// TestDeliveryDeployPostsACommitStatusAndMovesTheDeployedTag is what makes a deploy visible
// where Gitea already looks: the SHA's status list and a tag. Both are written by the
// notifier, the fork's single capture point, so a hook that stopped firing fails this.
func TestDeliveryDeployPostsACommitStatusAndMovesTheDeployedTag(t *testing.T) {
	// Writing a commit goes through Gitea's push path, whose pre-receive hook calls back
	// into the internal API, so the server has to be running.
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: user.ID})

		first := testCreateFileInBranch(t, user, repo, createFileInBranchOptions{
			OldBranch: repo.DefaultBranch, CommitMessage: "first deployable commit",
		}, map[string]string{"deploy-one.txt": "one"})
		require.NotNil(t, first.Commit)
		firstSHA := first.Commit.SHA

		deployAtSHA(t, repo, user, "sandbox", "v1.0", firstSHA, 9501, actions_model.StatusSuccess)

		statuses, err := git_model.GetLatestCommitStatus(t.Context(), repo.ID, firstSHA, db.ListOptions{ListAll: true})
		require.NoError(t, err)
		var deployStatus *git_model.CommitStatus
		for _, status := range statuses {
			if status.Context == "deploy/sandbox" {
				deployStatus = status
			}
		}
		require.NotNil(t, deployStatus, "the deploy posts a deploy/<env> status on the release's own SHA")
		assert.Equal(t, commitstatus.CommitStatusSuccess, deployStatus.State)
		assert.Contains(t, deployStatus.Description, "v1.0", "the status names the release it deployed")
		assert.Contains(t, deployStatus.Description, "sandbox")

		gitRepo, err := git.OpenRepository(t.Context(), repo)
		require.NoError(t, err)
		defer gitRepo.Close()

		tagged, err := gitRepo.GetTagCommitID(t.Context(), "deployed/sandbox")
		require.NoError(t, err, "a successful deploy leaves deployed/<env> at the SHA it deployed")
		assert.Equal(t, firstSHA, tagged)

		// A second deploy moves the tag: it names what is deployed now, not the first thing
		// ever deployed.
		second := testCreateFileInBranch(t, user, repo, createFileInBranchOptions{
			OldBranch: repo.DefaultBranch, CommitMessage: "second deployable commit",
		}, map[string]string{"deploy-two.txt": "two"})
		require.NotNil(t, second.Commit)
		deployAtSHA(t, repo, user, "sandbox", "v1.1", second.Commit.SHA, 9502, actions_model.StatusSuccess)

		tagged, err = gitRepo.GetTagCommitID(t.Context(), "deployed/sandbox")
		require.NoError(t, err)
		assert.Equal(t, second.Commit.SHA, tagged, "the tag follows the latest successful deploy")
	})
}

// TestDeliveryFailedDeployPostsAFailureAndLeavesTheTagAlone is the other half: a failure is
// reported where a success would be, and nothing claims the commit is deployed.
func TestDeliveryFailedDeployPostsAFailureAndLeavesTheTagAlone(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: user.ID})

		created := testCreateFileInBranch(t, user, repo, createFileInBranchOptions{
			OldBranch: repo.DefaultBranch, CommitMessage: "a commit whose deploy fails",
		}, map[string]string{"deploy-fail.txt": "fail"})
		require.NotNil(t, created.Commit)
		sha := created.Commit.SHA

		deployAtSHA(t, repo, user, "live", "v2.0", sha, 9503, actions_model.StatusFailure)

		statuses, err := git_model.GetLatestCommitStatus(t.Context(), repo.ID, sha, db.ListOptions{ListAll: true})
		require.NoError(t, err)
		var deployStatus *git_model.CommitStatus
		for _, status := range statuses {
			if status.Context == "deploy/live" {
				deployStatus = status
			}
		}
		require.NotNil(t, deployStatus, "a failed deploy is reported where a successful one would be")
		assert.Equal(t, commitstatus.CommitStatusFailure, deployStatus.State)

		gitRepo, err := git.OpenRepository(t.Context(), repo)
		require.NoError(t, err)
		defer gitRepo.Close()
		_, err = gitRepo.GetTagCommitID(t.Context(), "deployed/live")
		require.Error(t, err, "a failed deploy leaves no tag saying the commit is deployed")
	})
}

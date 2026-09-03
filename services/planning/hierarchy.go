// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
)

// maxHierarchyDepth is the deepest a subtree may run, counted from its root at depth 0.
const maxHierarchyDepth = 8

// maxSubtreeNodes caps how many nodes Subtree walks, so a cycle in stored data — which
// SetIssueParent itself refuses to create, but a hand-edited database might still hold —
// cannot hang a caller reading it back.
const maxSubtreeNodes = 2000

// RankAllows is hierarchy's ordering rule: a parent's type must outrank its child's — a lower
// rank number, since rank 1 is the highest a type may claim.
func RankAllows(parentRank, childRank int) bool { return parentRank < childRank }

// Progress is one parent's children rolled up to a count.
type Progress struct {
	Total  int `json:"total"`
	Closed int `json:"closed"`
}

// typesOf resolves the type actually assigned to each of issueIDs, absent for one with none.
func typesOf(ctx context.Context, issueIDs []int64) (map[int64]*planning_model.IssueType, error) {
	byIssue, err := planning_model.AssignmentsFor(ctx, issueIDs)
	if err != nil {
		return nil, err
	}
	typeIDs := make([]int64, 0, len(byIssue))
	for _, typeID := range byIssue {
		typeIDs = append(typeIDs, typeID)
	}
	types, err := planning_model.GetIssueTypesByIDs(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*planning_model.IssueType, len(byIssue))
	for issueID, typeID := range byIssue {
		if t, ok := types[typeID]; ok {
			out[issueID] = t
		}
	}
	return out, nil
}

// SetIssueParent records child's parent, refusing every way the two issues cannot be linked:
// the same issue, a different repository, either side a pull request, either side untyped,
// the parent's type failing to outrank the child's, the parent already sitting in the child's
// own subtree, or a resulting depth past maxHierarchyDepth.
func SetIssueParent(ctx context.Context, child, parent *issues_model.Issue) error {
	if child.ID == parent.ID {
		return &hub_model.Error{
			Code: "same_issue", Status: http.StatusUnprocessableEntity,
			Message:         "an issue cannot be its own parent",
			SuggestedAction: "Choose a different issue as the parent.",
		}
	}
	if child.RepoID != parent.RepoID {
		return &hub_model.Error{
			Code: "cross_repo", Status: http.StatusUnprocessableEntity,
			Message:         "the parent must belong to the same repository as the child",
			SuggestedAction: "Choose a parent from the same repository.",
		}
	}
	if child.IsPull || parent.IsPull {
		who := "the child"
		if parent.IsPull {
			who = "the parent"
		}
		return &hub_model.Error{
			Code: "pull_request", Status: http.StatusUnprocessableEntity,
			Message:         who + " is a pull request; hierarchy links issues only",
			SuggestedAction: "Link the underlying issue, not the pull request.",
		}
	}

	types, err := typesOf(ctx, []int64{child.ID, parent.ID})
	if err != nil {
		return err
	}
	childType, childTyped := types[child.ID]
	parentType, parentTyped := types[parent.ID]
	if !childTyped || !parentTyped {
		who := "the child"
		switch {
		case !childTyped && !parentTyped:
			who = "the child and the parent"
		case !parentTyped:
			who = "the parent"
		}
		return &hub_model.Error{
			Code: "untyped_issue", Status: http.StatusUnprocessableEntity,
			Message:         who + " carries no type, and hierarchy needs one on both sides to rank them",
			SuggestedAction: "Assign a type to " + who + " first.",
		}
	}
	if !RankAllows(parentType.Rank, childType.Rank) {
		return &hub_model.Error{
			Code: "rank_mismatch", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("%s (rank %d) does not outrank %s (rank %d)",
				parentType.Name, parentType.Rank, childType.Name, childType.Rank),
			SuggestedAction: "Choose a parent whose type outranks the child's, or change one of the two types.",
		}
	}

	parents, err := planning_model.ParentMapForRepo(ctx, child.RepoID)
	if err != nil {
		return err
	}
	subtree := Subtree(parents, child.ID)
	if slices.Contains(subtree, parent.ID) {
		return &hub_model.Error{
			Code: "cycle", Status: http.StatusUnprocessableEntity,
			Message:         "that parent is already a descendant of this issue",
			SuggestedAction: "Choose a parent outside this issue's own subtree.",
		}
	}

	depths := Depths(parents)
	newChildDepth := depths[parent.ID] + 1
	delta := newChildDepth - depths[child.ID]
	deepest := depths[child.ID]
	for _, id := range subtree {
		if depths[id] > deepest {
			deepest = depths[id]
		}
	}
	if deepest+delta > maxHierarchyDepth {
		return &hub_model.Error{
			Code: "too_deep", Status: http.StatusUnprocessableEntity,
			Message:         fmt.Sprintf("linking here would put a node in this subtree past depth %d", maxHierarchyDepth),
			SuggestedAction: "Choose a shallower parent, or split the subtree first.",
		}
	}

	return planning_model.UpsertParent(ctx, child.ID, parent.ID)
}

// RemoveIssueParent removes childID's recorded parent, if any.
func RemoveIssueParent(ctx context.Context, childID int64) error {
	return planning_model.DeleteParent(ctx, childID)
}

// Depths computes every node's distance from its own root, 0 for a root. It assumes parents
// holds no cycle — SetIssueParent refuses to create one — but still breaks out of one rather
// than looping forever, should stored data hold one anyway.
func Depths(parents map[int64]int64) map[int64]int {
	depth := make(map[int64]int, len(parents))
	var resolve func(id int64, visiting map[int64]bool) int
	resolve = func(id int64, visiting map[int64]bool) int {
		if d, ok := depth[id]; ok {
			return d
		}
		parentID, hasParent := parents[id]
		if !hasParent || visiting[id] {
			depth[id] = 0
			return 0
		}
		visiting[id] = true
		d := resolve(parentID, visiting) + 1
		delete(visiting, id)
		depth[id] = d
		return d
	}
	for id := range parents {
		resolve(id, map[int64]bool{})
	}
	return depth
}

// RootOf walks id's parent chain to its root. A cycle stops the walk at the first id seen
// twice rather than looping forever.
func RootOf(parents map[int64]int64, id int64) int64 {
	seen := map[int64]bool{}
	for {
		parentID, has := parents[id]
		if !has || seen[id] {
			return id
		}
		seen[id] = true
		id = parentID
	}
}

// Subtree lists root's descendants (never root itself) by breadth, capped at maxSubtreeNodes
// and safe against a cycle: a node is queued at most once.
func Subtree(parents map[int64]int64, root int64) []int64 {
	children := make(map[int64][]int64, len(parents))
	for child, parent := range parents {
		children[parent] = append(children[parent], child)
	}
	visited := map[int64]bool{root: true}
	queue := []int64{root}
	out := make([]int64, 0, 8)
	for len(queue) > 0 && len(visited) < maxSubtreeNodes {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if visited[c] {
				continue
			}
			visited[c] = true
			out = append(out, c)
			queue = append(queue, c)
			if len(visited) >= maxSubtreeNodes {
				break
			}
		}
	}
	return out
}

// ChildProgress rolls up every parent in parentIDs to its children's close count, in one query.
func ChildProgress(ctx context.Context, parentIDs []int64) (map[int64]Progress, error) {
	out := map[int64]Progress{}
	if len(parentIDs) == 0 {
		return out, nil
	}
	type row struct {
		ParentIssueID int64
		Total         int
		Closed        int
	}
	rows := make([]row, 0, len(parentIDs))
	err := db.GetEngine(ctx).Table("plan_issue_parent").
		Select("plan_issue_parent.parent_issue_id AS parent_issue_id, COUNT(*) AS total, "+
			"SUM(CASE WHEN issue.is_closed THEN 1 ELSE 0 END) AS closed").
		Join("INNER", "issue", "issue.id = plan_issue_parent.child_issue_id").
		In("plan_issue_parent.parent_issue_id", parentIDs).
		GroupBy("plan_issue_parent.parent_issue_id").
		Find(&rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ParentIssueID] = Progress{Total: r.Total, Closed: r.Closed}
	}
	return out, nil
}

// ParentMap is a thin wrapper over the model, so callers outside models/planning read the
// repo's parent edges through this package alone, like every other planning read.
func ParentMap(ctx context.Context, repoID int64) (map[int64]int64, error) {
	return planning_model.ParentMapForRepo(ctx, repoID)
}

// ParentsOf and ChildrenOf are thin wrappers over the model, for the same reason as ParentMap.
func ParentsOf(ctx context.Context, issueIDs []int64) (map[int64]int64, error) {
	return planning_model.ParentsOf(ctx, issueIDs)
}

func ChildrenOf(ctx context.Context, parentIDs []int64) (map[int64][]int64, error) {
	return planning_model.ChildrenOf(ctx, parentIDs)
}

// HasLinks is a thin wrapper over the model.
func HasLinks(ctx context.Context, issueID int64) (bool, error) {
	return planning_model.HasLinks(ctx, issueID)
}

package monitor

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

type fakeGitHubAPIClient struct {
	search      []githubIssueRecord
	prs         map[string]githubPullRequestRecord
	commentPRs  []int
	commentRows []githubReviewCommentRecord
	reviewPRs   []int
	reviewRows  []githubReviewRecord
}

func (f fakeGitHubAPIClient) SearchIssues(_ context.Context, query string) ([]githubIssueRecord, error) {
	if !strings.Contains(query, "assignee:") {
		return nil, nil
	}
	return f.search, nil
}

func (f fakeGitHubAPIClient) GetPullRequest(_ context.Context, owner, repo string, number int) (githubPullRequestRecord, error) {
	key := owner + "/" + repo + "#" + strconv.Itoa(number)
	if pr, ok := f.prs[key]; ok {
		return pr, nil
	}
	for k, pr := range f.prs {
		if strings.EqualFold(k, key) {
			return pr, nil
		}
	}
	return githubPullRequestRecord{}, nil
}

func (f *fakeGitHubAPIClient) ListReviewComments(_ context.Context, _ string, _ string, number int, _ string) ([]githubReviewCommentRecord, error) {
	f.commentPRs = append(f.commentPRs, number)
	return f.commentRows, nil
}

func (f *fakeGitHubAPIClient) ListReviews(_ context.Context, _ string, _ string, number int, _ string) ([]githubReviewRecord, error) {
	f.reviewPRs = append(f.reviewPRs, number)
	return f.reviewRows, nil
}

func TestGitHubPollerEnrichesPullRequestRefs(t *testing.T) {
	p := GitHubPoller{
		Client: &fakeGitHubAPIClient{
			search: []githubIssueRecord{
				{
					Number:        42,
					Title:         "Add GitHub integration",
					HTMLURL:       "https://github.com/Facets-cloud/flow-manager/pull/42",
					RepositoryURL: "https://api.github.com/repos/Facets-cloud/flow-manager",
					PullRequest:   []byte(`{"url":"https://api.github.com/repos/Facets-cloud/flow-manager/pulls/42"}`),
					User:          githubUser{Login: "octo"},
				},
			},
			prs: map[string]githubPullRequestRecord{
				"Facets-cloud/flow-manager#42": {
					Base: githubRef{Name: "main"},
					Head: githubRef{Name: "feature/github"},
				},
			},
		},
		SelfLogins: []string{"me"},
	}

	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].BaseRef != "main" || events[0].HeadRef != "feature/github" {
		t.Fatalf("refs = base %q head %q", events[0].BaseRef, events[0].HeadRef)
	}
}

func TestGitHubPollerFetchesReviewCommentsForTrackedPRNumber(t *testing.T) {
	db := dispatcherTestDB(t)
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")
	client := &fakeGitHubAPIClient{
		commentRows: []githubReviewCommentRecord{
			{NodeID: "PRRC_1", Body: "Please fix docs.", User: githubUser{Login: "reviewer"}},
		},
	}
	p := GitHubPoller{
		DB:         db,
		Client:     client,
		SelfLogins: []string{"me"},
	}

	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(client.commentPRs) != 1 || client.commentPRs[0] != 42 {
		t.Fatalf("comment PR calls = %v, want [42]", client.commentPRs)
	}
	if len(events) != 1 || events[0].Kind != GitHubEventPRReviewComment {
		t.Fatalf("events = %#v", events)
	}
}

func TestGitHubPollerFetchesReviewsForTrackedPRNumber(t *testing.T) {
	db := dispatcherTestDB(t)
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")
	client := &fakeGitHubAPIClient{
		prs: map[string]githubPullRequestRecord{
			"Facets-cloud/flow-manager#42": {
				State: "open",
			},
		},
		reviewRows: []githubReviewRecord{
			{
				NodeID:      "PRR_kwDOAAABBB",
				State:       "CHANGES_REQUESTED",
				Body:        "Please add a regression test.",
				HTMLURL:     "https://github.com/Facets-cloud/flow-manager/pull/42#pullrequestreview-44",
				User:        githubUser{Login: "reviewer"},
				SubmittedAt: "2026-05-23T10:00:00Z",
			},
		},
	}
	p := GitHubPoller{
		DB:         db,
		Client:     client,
		SelfLogins: []string{"me"},
	}

	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(client.reviewPRs) != 1 || client.reviewPRs[0] != 42 {
		t.Fatalf("review PR calls = %v, want [42]", client.reviewPRs)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Kind != GitHubEventPRReviewChangesRequested {
		t.Fatalf("kind = %q, want %q", events[0].Kind, GitHubEventPRReviewChangesRequested)
	}
	if events[0].EventKey != "review:PRR_kwDOAAABBB" {
		t.Fatalf("event key = %q", events[0].EventKey)
	}
	if events[0].URL != "https://github.com/Facets-cloud/flow-manager/pull/42#pullrequestreview-44" {
		t.Fatalf("url = %q", events[0].URL)
	}
}

func TestGitHubPollerEmitsHeadUpdatedForTrackedPR(t *testing.T) {
	db := dispatcherTestDB(t)
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")
	client := &fakeGitHubAPIClient{
		prs: map[string]githubPullRequestRecord{
			"Facets-cloud/flow-manager#42": {
				State: "open",
				Head:  githubRef{Name: "feature/github", SHA: "abc123"},
			},
		},
	}
	p := GitHubPoller{DB: db, Client: client, SelfLogins: []string{"me"}}

	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Kind != GitHubEventPRHeadUpdated || events[0].HeadSHA != "abc123" {
		t.Fatalf("head update event = %#v", events[0])
	}
}

func TestGitHubPollerEmitsMergedAndSkipsCommentsForMergedTrackedPR(t *testing.T) {
	db := dispatcherTestDB(t)
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")
	client := &fakeGitHubAPIClient{
		prs: map[string]githubPullRequestRecord{
			"Facets-cloud/flow-manager#42": {
				State:  "closed",
				Merged: true,
				Head:   githubRef{Name: "feature/github", SHA: "abc123"},
			},
		},
		commentRows: []githubReviewCommentRecord{
			{NodeID: "PRRC_1", Body: "stale comment"},
		},
	}
	p := GitHubPoller{DB: db, Client: client, SelfLogins: []string{"me"}}

	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(client.commentPRs) != 0 {
		t.Fatalf("merged PR should not fetch comments, got calls %v", client.commentPRs)
	}
	if len(events) != 1 || events[0].Kind != GitHubEventPRMerged {
		t.Fatalf("events = %#v", events)
	}
}

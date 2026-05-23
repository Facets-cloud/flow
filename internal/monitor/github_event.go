package monitor

import (
	"fmt"
	"strings"
)

type GitHubEventKind string

const (
	GitHubEventPRAssigned               GitHubEventKind = "pr_assigned"
	GitHubEventPRReviewRequested        GitHubEventKind = "pr_review_requested"
	GitHubEventPRReviewComment          GitHubEventKind = "pr_review_comment"
	GitHubEventPRReviewChangesRequested GitHubEventKind = "pr_review_changes_requested"
	GitHubEventPRReviewApproved         GitHubEventKind = "pr_review_approved"
	GitHubEventPRHeadUpdated            GitHubEventKind = "pr_head_updated"
	GitHubEventPRMerged                 GitHubEventKind = "pr_merged"
	GitHubEventIssueAssigned            GitHubEventKind = "issue_assigned"
)

type GitHubEvent struct {
	Kind      GitHubEventKind
	Owner     string
	Repo      string
	Number    int
	Title     string
	Body      string
	URL       string
	Author    string
	BaseRef   string
	HeadRef   string
	HeadSHA   string
	Labels    []string
	Milestone string
	CommentID string
	EventKey  string
	RawJSON   string
	CreatedAt string
	UpdatedAt string
}

func (ev GitHubEvent) RepoKey() string {
	owner := strings.Trim(strings.TrimSpace(ev.Owner), "/")
	repo := strings.Trim(strings.TrimSpace(ev.Repo), "/")
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
}

func (ev GitHubEvent) IsPR() bool {
	return ev.Kind == GitHubEventPRAssigned ||
		ev.Kind == GitHubEventPRReviewRequested ||
		ev.Kind == GitHubEventPRReviewComment ||
		ev.Kind == GitHubEventPRReviewChangesRequested ||
		ev.Kind == GitHubEventPRReviewApproved ||
		ev.Kind == GitHubEventPRHeadUpdated ||
		ev.Kind == GitHubEventPRMerged
}

func (ev GitHubEvent) LinkTag() string {
	if ev.Number <= 0 || ev.RepoKey() == "" {
		return ""
	}
	if ev.Kind == GitHubEventIssueAssigned {
		return fmt.Sprintf("gh-issue:%s#%d", ev.RepoKey(), ev.Number)
	}
	return fmt.Sprintf("gh-pr:%s#%d", ev.RepoKey(), ev.Number)
}

func (ev GitHubEvent) EventKeyValue() string {
	if key := strings.TrimSpace(ev.EventKey); key != "" {
		return key
	}
	if ev.LinkTag() == "" {
		return ""
	}
	if ev.CommentID != "" {
		return "review-comment:" + ev.CommentID
	}
	return fmt.Sprintf("%s:%s", ev.LinkTag(), ev.Kind)
}

func gitHubPRHeadEventKey(owner, repo string, number int, sha string) string {
	owner = strings.Trim(strings.TrimSpace(owner), "/")
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	sha = strings.TrimSpace(sha)
	if owner == "" || repo == "" || number <= 0 || sha == "" {
		return ""
	}
	return fmt.Sprintf("pr-head:%s/%s#%d:%s", owner, repo, number, sha)
}

func GitHubSlugForEvent(ev GitHubEvent) string {
	prefix := "gh-pr-"
	if ev.Kind == GitHubEventIssueAssigned {
		prefix = "gh-issue-"
	}
	base := strings.ToLower(strings.TrimSpace(ev.RepoKey()))
	if base == "" || ev.Number <= 0 {
		return ""
	}
	return sanitizeGitHubSlug(fmt.Sprintf("%s%s-%d", prefix, base, ev.Number))
}

func sanitizeGitHubSlug(s string) string {
	out := strings.Builder{}
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		keep := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if keep {
			out.WriteRune(r)
			prevDash = false
			continue
		}
		if r == '-' || r == '_' || r == '/' || r == ':' || r == '.' || r == '#' {
			if !prevDash {
				out.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}

func ProviderForGitHubLabels(labels []string) string {
	for _, label := range labels {
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "flow:codex":
			return "codex"
		case "flow:claude":
			return "claude"
		}
	}
	return "claude"
}

func gitHubEventToInboxEvent(ev GitHubEvent) InboundEvent {
	return InboundEvent{
		Kind:        string(ev.Kind),
		Channel:     ev.RepoKey(),
		ChannelType: "github",
		TS:          firstNonEmpty(ev.UpdatedAt, ev.CreatedAt),
		ThreadTS:    ev.LinkTag(),
		UserID:      strings.TrimSpace(ev.Author),
		Text:        strings.TrimSpace(ev.Body),
		URL:         strings.TrimSpace(ev.URL),
		ItemChannel: ev.RepoKey(),
		ItemTS:      fmt.Sprintf("%d", ev.Number),
		ItemAuthor:  strings.TrimSpace(ev.Author),
		RawJSON:     strings.TrimSpace(ev.RawJSON),
	}
}

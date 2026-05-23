package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type GitHubPoller struct {
	DB         *sql.DB
	Client     GitHubAPIClient
	SelfLogins []string
	Repos      []string
}

type GitHubAPIClient interface {
	SearchIssues(ctx context.Context, query string) ([]githubIssueRecord, error)
	GetPullRequest(ctx context.Context, owner, repo string, number int) (githubPullRequestRecord, error)
	ListReviewComments(ctx context.Context, owner, repo string, number int, since string) ([]githubReviewCommentRecord, error)
	ListReviews(ctx context.Context, owner, repo string, number int, since string) ([]githubReviewRecord, error)
}

func GitHubPollingEnabled() bool {
	return envBoolDefault("FLOW_GH_ENABLED", false) && len(GitHubSelfLogins()) > 0
}

func GitHubSelfLogins() []string {
	return splitList(os.Getenv("FLOW_GH_SELF_LOGINS"))
}

func GitHubRepos() []string {
	return splitList(os.Getenv("FLOW_GH_REPOS"))
}

func GitHubPollInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("FLOW_GH_POLL_INTERVAL"))
	if raw == "" {
		return time.Minute
	}
	d, err := time.ParseDuration(raw)
	if err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return time.Minute
}

func splitList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		k := strings.ToLower(v)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

func (p GitHubPoller) Poll(ctx context.Context) ([]GitHubEvent, error) {
	if p.Client == nil {
		p.Client = ghAPIClient{}
	}
	var events []GitHubEvent
	seen := map[string]bool{}
	add := func(ev GitHubEvent) {
		key := ev.EventKeyValue()
		if key == "" {
			key = string(ev.Kind) + ":" + ev.LinkTag()
		}
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		events = append(events, ev)
	}
	for _, login := range p.SelfLogins {
		for _, query := range p.queriesForLogin(login) {
			records, err := p.Client.SearchIssues(ctx, query)
			if err != nil {
				return events, err
			}
			for _, rec := range records {
				ev, ok := rec.toGitHubEvent(login, query)
				if ok {
					if ev.IsPR() {
						pr, err := p.Client.GetPullRequest(ctx, ev.Owner, ev.Repo, ev.Number)
						if err != nil {
							return events, err
						}
						ev.BaseRef = pr.Base.Name
						ev.HeadRef = pr.Head.Name
						ev.HeadSHA = pr.Head.SHA
					}
					add(ev)
				}
			}
		}
	}
	comments, err := p.pollTrackedPRComments(ctx)
	if err != nil {
		return events, err
	}
	for _, ev := range comments {
		add(ev)
	}
	return events, nil
}

func (p GitHubPoller) queriesForLogin(login string) []string {
	login = strings.TrimSpace(login)
	if login == "" {
		return nil
	}
	base := []string{
		"is:open assignee:" + login,
		"is:open is:pr review-requested:" + login,
	}
	if len(p.Repos) == 0 {
		return base
	}
	out := make([]string, 0, len(base)*len(p.Repos))
	for _, repo := range p.Repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		for _, q := range base {
			out = append(out, q+" repo:"+repo)
		}
	}
	return out
}

func (p GitHubPoller) pollTrackedPRComments(ctx context.Context) ([]GitHubEvent, error) {
	if p.DB == nil {
		return nil, nil
	}
	prs, err := trackedGitHubPRs(p.DB)
	if err != nil {
		return nil, err
	}
	var events []GitHubEvent
	for _, pr := range prs {
		detail, err := p.Client.GetPullRequest(ctx, pr.Owner, pr.Repo, pr.Number)
		if err != nil {
			return events, err
		}
		if detail.Merged {
			events = append(events, GitHubEvent{
				Kind:     GitHubEventPRMerged,
				Owner:    pr.Owner,
				Repo:     pr.Repo,
				Number:   pr.Number,
				BaseRef:  detail.Base.Name,
				HeadRef:  detail.Head.Name,
				HeadSHA:  detail.Head.SHA,
				Body:     fmt.Sprintf("Pull request %s/%s#%d was merged.", pr.Owner, pr.Repo, pr.Number),
				EventKey: fmt.Sprintf("pr-merged:%s/%s#%d", pr.Owner, pr.Repo, pr.Number),
			})
			continue
		}
		if detail.State != "" && !strings.EqualFold(detail.State, "open") {
			continue
		}
		if detail.Head.SHA != "" {
			events = append(events, GitHubEvent{
				Kind:     GitHubEventPRHeadUpdated,
				Owner:    pr.Owner,
				Repo:     pr.Repo,
				Number:   pr.Number,
				BaseRef:  detail.Base.Name,
				HeadRef:  detail.Head.Name,
				HeadSHA:  detail.Head.SHA,
				Body:     fmt.Sprintf("Pull request head changed to %s (%s). Review the PR again.", shortGitHubSHA(detail.Head.SHA), nonEmptyOr(detail.Head.Name, "unknown head")),
				EventKey: gitHubPRHeadEventKey(pr.Owner, pr.Repo, pr.Number, detail.Head.SHA),
			})
		}
		comments, err := p.Client.ListReviewComments(ctx, pr.Owner, pr.Repo, pr.Number, "")
		if err != nil {
			return events, err
		}
		for _, c := range comments {
			ev, ok := c.toGitHubEvent(pr.Owner, pr.Repo, pr.Number)
			if ok {
				events = append(events, ev)
			}
		}
		reviews, err := p.Client.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number, "")
		if err != nil {
			return events, err
		}
		for _, r := range reviews {
			ev, ok := r.toGitHubEvent(pr.Owner, pr.Repo, pr.Number)
			if ok {
				events = append(events, ev)
			}
		}
	}
	return events, nil
}

func shortGitHubSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

type trackedGitHubPR struct {
	Owner  string
	Repo   string
	Number int
}

func trackedGitHubPRs(db *sql.DB) ([]trackedGitHubPR, error) {
	rows, err := db.Query(`SELECT DISTINCT tag FROM task_tags WHERE tag LIKE 'gh-pr:%'`)
	if err != nil {
		return nil, fmt.Errorf("list tracked github prs: %w", err)
	}
	defer rows.Close()
	var out []trackedGitHubPR
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		pr, ok := parseGitHubPRTag(tag)
		if ok {
			out = append(out, pr)
		}
	}
	return out, rows.Err()
}

func parseGitHubPRTag(tag string) (trackedGitHubPR, bool) {
	raw := strings.TrimPrefix(strings.TrimSpace(tag), "gh-pr:")
	if raw == tag || raw == "" {
		return trackedGitHubPR{}, false
	}
	repo, numText, ok := strings.Cut(raw, "#")
	if !ok {
		return trackedGitHubPR{}, false
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return trackedGitHubPR{}, false
	}
	n, err := strconv.Atoi(numText)
	if err != nil || n <= 0 {
		return trackedGitHubPR{}, false
	}
	return trackedGitHubPR{Owner: owner, Repo: name, Number: n}, true
}

type ghAPIClient struct{}

func (ghAPIClient) SearchIssues(ctx context.Context, query string) ([]githubIssueRecord, error) {
	out, err := exec.CommandContext(ctx, "gh", "api", "-X", "GET", "search/issues", "-f", "q="+query, "-f", "per_page=100").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh api search/issues: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	var resp githubIssueSearchResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse github search response: %w", err)
	}
	return resp.Items, nil
}

func (ghAPIClient) GetPullRequest(ctx context.Context, owner, repo string, number int) (githubPullRequestRecord, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number)
	out, err := exec.CommandContext(ctx, "gh", "api", endpoint).CombinedOutput()
	if err != nil {
		return githubPullRequestRecord{}, fmt.Errorf("gh api %s: %w (output: %s)", endpoint, err, strings.TrimSpace(string(out)))
	}
	var resp githubPullRequestRecord
	if err := json.Unmarshal(out, &resp); err != nil {
		return githubPullRequestRecord{}, fmt.Errorf("parse github pull request: %w", err)
	}
	return resp, nil
}

func (ghAPIClient) ListReviewComments(ctx context.Context, owner, repo string, number int, since string) ([]githubReviewCommentRecord, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/comments", owner, repo, number)
	args := []string{"api", "-X", "GET", endpoint, "-f", "per_page=100", "-f", "sort=updated", "-f", "direction=desc"}
	if strings.TrimSpace(since) != "" {
		args = append(args, "-f", "since="+strings.TrimSpace(since))
	}
	out, err := exec.CommandContext(ctx, "gh", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %w (output: %s)", endpoint, err, strings.TrimSpace(string(out)))
	}
	var resp []githubReviewCommentRecord
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse github review comments: %w", err)
	}
	return resp, nil
}

func (ghAPIClient) ListReviews(ctx context.Context, owner, repo string, number int, since string) ([]githubReviewRecord, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, number)
	out, err := exec.CommandContext(ctx, "gh", "api", endpoint, "-f", "per_page=100").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %w (output: %s)", endpoint, err, strings.TrimSpace(string(out)))
	}
	var resp []githubReviewRecord
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse github reviews: %w", err)
	}
	since = strings.TrimSpace(since)
	if since == "" {
		return resp, nil
	}
	filtered := make([]githubReviewRecord, 0, len(resp))
	for _, record := range resp {
		if strings.TrimSpace(record.SubmittedAt) > since {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

type githubIssueSearchResponse struct {
	Items []githubIssueRecord `json:"items"`
}

type githubIssueRecord struct {
	Number        int              `json:"number"`
	Title         string           `json:"title"`
	Body          string           `json:"body"`
	HTMLURL       string           `json:"html_url"`
	RepositoryURL string           `json:"repository_url"`
	PullRequest   json.RawMessage  `json:"pull_request"`
	User          githubUser       `json:"user"`
	Labels        []githubLabel    `json:"labels"`
	Milestone     *githubMilestone `json:"milestone"`
	CreatedAt     string           `json:"created_at"`
	UpdatedAt     string           `json:"updated_at"`
	Raw           json.RawMessage  `json:"-"`
}

type githubUser struct {
	Login string `json:"login"`
}

type githubLabel struct {
	Name string `json:"name"`
}

type githubMilestone struct {
	Title string `json:"title"`
}

type githubPullRequestRecord struct {
	State  string    `json:"state"`
	Merged bool      `json:"merged"`
	Base   githubRef `json:"base"`
	Head   githubRef `json:"head"`
}

type githubRef struct {
	Name string `json:"ref"`
	SHA  string `json:"sha"`
}

func (r githubIssueRecord) toGitHubEvent(login, query string) (GitHubEvent, bool) {
	owner, repo := ownerRepoFromRepositoryURL(r.RepositoryURL)
	if owner == "" || repo == "" || r.Number <= 0 {
		return GitHubEvent{}, false
	}
	kind := GitHubEventIssueAssigned
	if len(r.PullRequest) > 0 && string(r.PullRequest) != "null" {
		kind = GitHubEventPRAssigned
		if strings.Contains(query, "review-requested:"+login) {
			kind = GitHubEventPRReviewRequested
		}
	}
	labels := make([]string, 0, len(r.Labels))
	for _, l := range r.Labels {
		if strings.TrimSpace(l.Name) != "" {
			labels = append(labels, l.Name)
		}
	}
	milestone := ""
	if r.Milestone != nil {
		milestone = r.Milestone.Title
	}
	raw, _ := json.Marshal(r)
	return GitHubEvent{
		Kind:      kind,
		Owner:     owner,
		Repo:      repo,
		Number:    r.Number,
		Title:     r.Title,
		Body:      r.Body,
		URL:       r.HTMLURL,
		Author:    r.User.Login,
		Labels:    labels,
		Milestone: milestone,
		EventKey:  fmt.Sprintf("%s:%s", linkTagForRecord(kind, owner, repo, r.Number), kind),
		RawJSON:   string(raw),
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}, true
}

func linkTagForRecord(kind GitHubEventKind, owner, repo string, number int) string {
	if kind == GitHubEventIssueAssigned {
		return fmt.Sprintf("gh-issue:%s/%s#%d", owner, repo, number)
	}
	return fmt.Sprintf("gh-pr:%s/%s#%d", owner, repo, number)
}

func ownerRepoFromRepositoryURL(u string) (string, string) {
	u = strings.TrimSpace(u)
	idx := strings.LastIndex(u, "/repos/")
	if idx >= 0 {
		u = u[idx+len("/repos/"):]
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

type githubReviewCommentRecord struct {
	ID             int64      `json:"id"`
	NodeID         string     `json:"node_id"`
	Body           string     `json:"body"`
	HTMLURL        string     `json:"html_url"`
	PullRequestURL string     `json:"pull_request_url"`
	User           githubUser `json:"user"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      string     `json:"updated_at"`
}

type githubReviewRecord struct {
	ID          int64      `json:"id"`
	NodeID      string     `json:"node_id"`
	State       string     `json:"state"`
	Body        string     `json:"body"`
	HTMLURL     string     `json:"html_url"`
	User        githubUser `json:"user"`
	SubmittedAt string     `json:"submitted_at"`
}

func (r githubReviewCommentRecord) toGitHubEvent(owner, repo string, number int) (GitHubEvent, bool) {
	if owner == "" || repo == "" || number <= 0 {
		return GitHubEvent{}, false
	}
	id := strings.TrimSpace(r.NodeID)
	if id == "" && r.ID > 0 {
		id = strconv.FormatInt(r.ID, 10)
	}
	if id == "" {
		return GitHubEvent{}, false
	}
	raw, _ := json.Marshal(r)
	return GitHubEvent{
		Kind:      GitHubEventPRReviewComment,
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		Body:      r.Body,
		URL:       r.HTMLURL,
		Author:    r.User.Login,
		CommentID: id,
		EventKey:  "review-comment:" + id,
		RawJSON:   string(raw),
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}, true
}

func (r githubReviewRecord) toGitHubEvent(owner, repo string, number int) (GitHubEvent, bool) {
	if owner == "" || repo == "" || number <= 0 {
		return GitHubEvent{}, false
	}
	id := strings.TrimSpace(r.NodeID)
	if id == "" && r.ID > 0 {
		id = strconv.FormatInt(r.ID, 10)
	}
	if id == "" {
		return GitHubEvent{}, false
	}
	var kind GitHubEventKind
	switch strings.ToUpper(strings.TrimSpace(r.State)) {
	case "CHANGES_REQUESTED":
		kind = GitHubEventPRReviewChangesRequested
	case "APPROVED":
		kind = GitHubEventPRReviewApproved
	case "COMMENTED":
		kind = GitHubEventPRReviewComment
	default:
		return GitHubEvent{}, false
	}
	raw, _ := json.Marshal(r)
	return GitHubEvent{
		Kind:      kind,
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		Body:      r.Body,
		URL:       r.HTMLURL,
		Author:    r.User.Login,
		CommentID: id,
		EventKey:  "review:" + id,
		RawJSON:   string(raw),
		CreatedAt: r.SubmittedAt,
		UpdatedAt: r.SubmittedAt,
	}, true
}

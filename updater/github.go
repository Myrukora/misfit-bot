package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ── GitHub API types ──────────────────────────────────────────────────────

type ghUser struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
}

type ghCommitAuthor struct {
	Name string `json:"name"`
	Date string `json:"date"`
}

type ghCommitDetails struct {
	Message string         `json:"message"`
	Author  ghCommitAuthor `json:"author"`
}

type ghCommit struct {
	SHA     string          `json:"sha"`
	HTMLURL string          `json:"html_url"`
	Commit  ghCommitDetails `json:"commit"`
	Author  *ghUser         `json:"author"` // null for commits without a linked GitHub account
}

type ghPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    ghUser `json:"user"`
	Draft   bool   `json:"draft"`
}

// ghAPI is the interface the Manager talks to for GitHub data. The real
// implementation is *githubClient; tests use a fake to exercise the diffing
// logic without network access.
type ghAPI interface {
	fetchRemoteHead(ctx context.Context) (string, error)
	fetchCommitsSince(ctx context.Context, base string) ([]ghCommit, error)
	fetchOpenPRs(ctx context.Context) ([]ghPR, error)
	fetchUser(ctx context.Context) (*ghUser, error)
}

// ── GitHub REST client (plain net/http, no dependencies) ─────────────────

type githubClient struct {
	repo   string // owner/name
	branch string
	token  string
	http   *http.Client
}

func newGitHubClient() *githubClient {
	return &githubClient{http: &http.Client{Timeout: 15 * time.Second}}
}

// do performs a GET on api.github.com and decodes the JSON response.
func (g *githubClient) do(ctx context.Context, path string, out any) error {
	url := "https://api.github.com" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github api %s: %s %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchRemoteHead returns the SHA of the tip of the tracked branch.
func (g *githubClient) fetchRemoteHead(ctx context.Context) (string, error) {
	var out []struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/commits?sha=%s&per_page=1", g.repo, g.branch)
	if err := g.do(ctx, path, &out); err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", fmt.Errorf("no commits found on %s", g.branch)
	}
	return out[0].SHA, nil
}

// fetchCommitsSince returns the commits on the tracked branch that are not in
// base (i.e. the commits since the last seen SHA), newest first, capped at 5.
// If the history was rewritten (force push), the compare fails and the caller
// resyncs to the remote head.
func (g *githubClient) fetchCommitsSince(ctx context.Context, base string) ([]ghCommit, error) {
	var out struct {
		AheadBy int        `json:"ahead_by"`
		Commits []ghCommit `json:"commits"`
	}
	path := fmt.Sprintf("/repos/%s/compare/%s...%s?per_page=5", g.repo, base, g.branch)
	if err := g.do(ctx, path, &out); err != nil {
		return nil, err
	}
	// The compare API lists commits base→head (oldest first); reverse so the
	// newest commit is notified first.
	commits := out.Commits
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// fetchOpenPRs returns the open pull requests, newest first, capped at 100.
func (g *githubClient) fetchOpenPRs(ctx context.Context) ([]ghPR, error) {
	var out []ghPR
	path := fmt.Sprintf("/repos/%s/pulls?state=open&sort=created&direction=desc&per_page=100", g.repo)
	if err := g.do(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// fetchUser returns the authenticated user (used by the embed tester to show a
// realistic author row).
func (g *githubClient) fetchUser(ctx context.Context) (*ghUser, error) {
	var out ghUser
	if err := g.do(ctx, "/user", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

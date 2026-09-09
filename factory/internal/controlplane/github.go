package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// This file is the control plane's first and only outbound network access.
// Before it, internal/controlplane contained no exec.Command and no HTTP
// client at all, and that was a real property worth being deliberate about
// giving up. INV-3 requires the SERVER to verify a delivery, and it cannot do
// that without asking GitHub, so the seam is here, narrow, and behind an
// interface so every test runs against a fake.

const (
	githubAPIRoot     = "https://api.github.com"
	githubPageSize    = 100
	githubMaxPages    = 10
	githubHTTPTimeout = 15 * time.Second
)

type pullRequestState struct {
	HeadRepo string
	HeadRef  string
	HeadSHA  string
	Merged   bool
	State    string
}

type githubClient interface {
	PullRequest(ctx context.Context, owner, repo string, number int) (pullRequestState, error)
	BranchRef(ctx context.Context, owner, repo, branch string) (string, error)
	// FailingChecks returns the names of check runs and commit statuses on sha
	// that are in a failing state. An empty slice means nothing is failing,
	// which includes the case where nothing ran at all. On a private repository
	// without branch protection there are no required checks, so zero checks
	// passes. That is the honest reading of INV-8's "checks pass", and it is
	// vacuously true today on the target repository.
	FailingChecks(ctx context.Context, owner, repo, sha string) ([]string, error)
	MergePullRequest(ctx context.Context, owner, repo string, number int, sha string) error
}

var errPullRequestURL = errors.New("not a GitHub pull request URL")

// parsePullRequestURL is a port of parseGitHubPullRequestURL,
// internal/worker/agent_update.go:423-440. The two must stay in step: if the
// worker rejects a URL the server accepts, or the reverse, evidence that
// passed one check fails the other for reasons neither reports usefully. That
// is why the host comparison is case insensitive and why the path segments are
// unescaped here exactly as they are there, even though neither is obvious.
func parsePullRequestURL(raw string) (owner, repo string, number int, err error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, errPullRequestURL
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") {
		return "", "", 0, errPullRequestURL
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", 0, errPullRequestURL
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", "", 0, errPullRequestURL
	}
	owner, ownerErr := url.PathUnescape(parts[0])
	repo, repoErr := url.PathUnescape(parts[1])
	number, numberErr := strconv.Atoi(parts[3])
	if ownerErr != nil || repoErr != nil || numberErr != nil || number < 1 ||
		strings.ContainsAny(owner+repo, "/\\") {
		return "", "", 0, errPullRequestURL
	}
	return owner, repo, number, nil
}

type restGitHub struct {
	token  string
	client *http.Client
}

func newRESTGitHub(token string) *restGitHub {
	return &restGitHub{token: token, client: &http.Client{Timeout: githubHTTPTimeout}}
}

func (g *restGitHub) do(ctx context.Context, method, path string, body, out any) error {
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(encoded))
	}
	var request *http.Request
	var err error
	if reader != nil {
		request, err = http.NewRequestWithContext(ctx, method, githubAPIRoot+path, reader)
	} else {
		request, err = http.NewRequestWithContext(ctx, method, githubAPIRoot+path, nil)
	}
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		request.Header.Set("Authorization", "Bearer "+g.token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := g.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var detail struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(response.Body).Decode(&detail)
		return fmt.Errorf("github %s %s: %d %s", method, path, response.StatusCode, detail.Message)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func (g *restGitHub) PullRequest(ctx context.Context, owner, repo string, number int) (pullRequestState, error) {
	var payload struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
		Head   struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	if err := g.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return pullRequestState{}, err
	}
	return pullRequestState{
		HeadRepo: payload.Head.Repo.FullName,
		HeadRef:  payload.Head.Ref,
		HeadSHA:  payload.Head.SHA,
		Merged:   payload.Merged,
		State:    payload.State,
	}, nil
}

func (g *restGitHub) BranchRef(ctx context.Context, owner, repo, branch string) (string, error) {
	var payload struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	// The ref path carries slashes on purpose: a branch named factory/work-abc
	// is the ref heads/factory/work-abc, so the segments are escaped one by one
	// rather than escaping the whole branch name into a single segment.
	path := fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s",
		url.PathEscape(owner), url.PathEscape(repo), escapeRefPath(branch))
	if err := g.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return "", err
	}
	return payload.Object.SHA, nil
}

func escapeRefPath(ref string) string {
	segments := strings.Split(ref, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func (g *restGitHub) FailingChecks(ctx context.Context, owner, repo, sha string) ([]string, error) {
	failing := []string{}
	escapedOwner, escapedRepo := url.PathEscape(owner), url.PathEscape(repo)
	escapedSHA := url.PathEscape(sha)

	// Both listings paginate. Walking them matters: a failing check that landed
	// on page two would otherwise be invisible, and this phase merges on what
	// this function reports. Running out of pages is itself reported as
	// failing, because the rule for this phase is to fail closed.
	for page := 1; ; page++ {
		if page > githubMaxPages {
			failing = append(failing, fmt.Sprintf(
				"more than %d check runs (not all read)", githubMaxPages*githubPageSize))
			break
		}
		var runs struct {
			TotalCount int `json:"total_count"`
			CheckRuns  []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}
		path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=%d&page=%d",
			escapedOwner, escapedRepo, escapedSHA, githubPageSize, page)
		if err := g.do(ctx, http.MethodGet, path, nil, &runs); err != nil {
			return nil, err
		}
		for _, run := range runs.CheckRuns {
			// A run still in progress is not failing, but it is not passing
			// either. Treat it as failing so the merge waits rather than
			// racing it. Failing closed is the rule for this phase.
			switch {
			case run.Status != "completed":
				failing = append(failing, run.Name+" (still running)")
			case run.Conclusion != "success" && run.Conclusion != "neutral" && run.Conclusion != "skipped":
				failing = append(failing, run.Name+" ("+run.Conclusion+")")
			}
		}
		if len(runs.CheckRuns) < githubPageSize {
			break
		}
	}

	for page := 1; ; page++ {
		if page > githubMaxPages {
			failing = append(failing, fmt.Sprintf(
				"more than %d commit statuses (not all read)", githubMaxPages*githubPageSize))
			break
		}
		var statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		}
		path := fmt.Sprintf("/repos/%s/%s/commits/%s/statuses?per_page=%d&page=%d",
			escapedOwner, escapedRepo, escapedSHA, githubPageSize, page)
		if err := g.do(ctx, http.MethodGet, path, nil, &statuses); err != nil {
			return nil, err
		}
		for _, entry := range statuses {
			if entry.State != "success" {
				failing = append(failing, entry.Context+" ("+entry.State+")")
			}
		}
		if len(statuses) < githubPageSize {
			break
		}
	}

	// The combined status roll-up reports "pending" when total_count is 0,
	// which is why the individual entries are read instead. On a repository
	// with no checks at all this correctly returns nothing failing.
	return failing, nil
}

func (g *restGitHub) MergePullRequest(ctx context.Context, owner, repo string, number int, sha string) error {
	// sha is required, not optional. It is the head SHA the server itself
	// verified, and passing it makes GitHub refuse with 409 if the branch moved
	// between verification and merge. Without it a race merges code nobody
	// verified.
	body := map[string]string{"sha": sha, "merge_method": "squash"}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge",
		url.PathEscape(owner), url.PathEscape(repo), number)
	return g.do(ctx, http.MethodPut, path, body, nil)
}

// loadGitHubToken reads a mode-0600 file. The server gets a credential of its
// own rather than borrowing the operator's gh login, because the fork supports
// off-host workers and co-location is a property of this deployment, not of
// the code.
func loadGitHubToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("github token file %s must not be group or world readable", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("github token file %s is empty", path)
	}
	return token, nil
}

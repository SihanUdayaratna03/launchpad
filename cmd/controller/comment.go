package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// Every comment the controller writes starts with this marker, so a later push
// edits the same comment instead of adding a new one.
const commentMarker = "<!-- launchpad-preview -->"

const (
	githubAPI        = "https://api.github.com"
	commentsPageSize = 100
	maxCommentPages  = 5
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type commenter struct {
	token   string
	baseURL string
	http    *http.Client
}

func newCommenter(token string) *commenter {
	if token == "" {
		return nil
	}
	return &commenter{token: token, baseURL: githubAPI, http: &http.Client{Timeout: 10 * time.Second}}
}

func shortSHA(sha string) string {
	if len(sha) < 7 {
		return sha
	}
	return sha[:7]
}

func readyBody(host, sha string) string {
	return fmt.Sprintf("%s\n🚀 **Preview ready:** http://%s\n\nCommit `%s`. It updates on every push and is destroyed when this pull request closes.",
		commentMarker, host, shortSHA(sha))
}

func deployingBody(sha string) string {
	return fmt.Sprintf("%s\n🚧 **Preview deploying** for commit `%s`.", commentMarker, shortSHA(sha))
}

func removedBody() string {
	return commentMarker + "\n🧹 **Preview destroyed.**"
}

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

func (c *commenter) do(ctx context.Context, method, path string, in, out interface{}) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("github %s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *commenter) findOurs(ctx context.Context, repo string, pr int) (int64, error) {
	for page := 1; page <= maxCommentPages; page++ {
		var comments []issueComment
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=%d&page=%d", repo, pr, commentsPageSize, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &comments); err != nil {
			return 0, err
		}
		for _, cm := range comments {
			if len(cm.Body) >= len(commentMarker) && cm.Body[:len(commentMarker)] == commentMarker {
				return cm.ID, nil
			}
		}
		if len(comments) < commentsPageSize {
			break
		}
	}
	return 0, nil
}

// upsert edits the controller's comment on the PR, or creates it. With
// createIfMissing false it only edits, so closing a PR never adds a comment.
func (c *commenter) upsert(ctx context.Context, repo string, pr int, body string, createIfMissing bool) error {
	if !repoPattern.MatchString(repo) {
		return fmt.Errorf("invalid repository name %q", repo)
	}

	id, err := c.findOurs(ctx, repo, pr)
	if err != nil {
		return err
	}

	payload := map[string]string{"body": body}
	if id != 0 {
		return c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repo, id), payload, nil)
	}
	if !createIfMissing {
		return nil
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, pr), payload, nil)
}

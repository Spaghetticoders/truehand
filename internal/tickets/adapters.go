package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Linear struct {
	Key      string
	Endpoint string
	Client   *http.Client
}

const linearQuery = `query($id: String!) { issue(id: $id) {
  identifier title url description priorityLabel
  state { name } assignee { name }
  labels { nodes { name } }
  comments(first: 100) { nodes { body createdAt user { name } } }
  attachments(first: 50) { nodes { title url } }
  parent { identifier title }
  children(first: 50) { nodes { identifier title } }
  relations(first: 50) { nodes { type relatedIssue { identifier title } } }
} }`

func (l *Linear) Fetch(ctx context.Context, ref string) (*Ticket, error) {
	endpoint := l.Endpoint
	if endpoint == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	body, _ := json.Marshal(map[string]any{"query": linearQuery, "variables": map[string]string{"id": ref}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.Key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Issue *struct {
				Identifier, Title, URL, Description, PriorityLabel string
				State                                              struct{ Name string }
				Assignee                                           *struct{ Name string }
				Labels                                             struct{ Nodes []struct{ Name string } }
				Comments                                           struct {
					Nodes []struct {
						Body      string
						CreatedAt time.Time
						User      *struct{ Name string }
					}
				}
				Attachments struct{ Nodes []struct{ Title, URL string } }
				Parent      *struct{ Identifier, Title string }
				Children    struct {
					Nodes []struct{ Identifier, Title string }
				}
				Relations struct {
					Nodes []struct {
						Type         string
						RelatedIssue struct{ Identifier, Title string }
					}
				}
			}
		}
		Errors []struct{ Message string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("linear: HTTP %d: %w", resp.StatusCode, err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("linear: %s", out.Errors[0].Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear: HTTP %d", resp.StatusCode)
	}
	is := out.Data.Issue
	if is == nil {
		return nil, fmt.Errorf("linear: issue %s not found", ref)
	}
	t := &Ticket{Ref: is.Identifier, Title: is.Title, URL: is.URL, State: is.State.Name, Description: is.Description}
	if is.Assignee != nil {
		t.Assignee = is.Assignee.Name
	}
	for _, l := range is.Labels.Nodes {
		t.Labels = append(t.Labels, l.Name)
	}
	for _, c := range is.Comments.Nodes {
		author := "unknown"
		if c.User != nil {
			author = c.User.Name
		}
		t.Comments = append(t.Comments, Comment{Author: author, Created: c.CreatedAt, Body: c.Body})
	}
	sortComments(t.Comments)
	for _, a := range is.Attachments.Nodes {
		t.Links = append(t.Links, Link{Title: a.Title, URL: a.URL})
	}
	if is.Parent != nil {
		t.Related = append(t.Related, Link{Title: "parent: " + is.Parent.Identifier + " " + is.Parent.Title})
	}
	for _, c := range is.Children.Nodes {
		t.Related = append(t.Related, Link{Title: "sub-issue: " + c.Identifier + " " + c.Title})
	}
	for _, r := range is.Relations.Nodes {
		t.Related = append(t.Related, Link{Title: r.Type + ": " + r.RelatedIssue.Identifier + " " + r.RelatedIssue.Title})
	}
	return t, nil
}

// GitHub uses the gh CLI, so it inherits the user's GitHub auth.
type GitHub struct{}

func (GitHub) Fetch(ctx context.Context, ref string) (*Ticket, error) {
	args := []string{"issue", "view", ref, "--json", "number,title,url,state,body,labels,assignees,comments"}
	if i := strings.LastIndex(ref, "#"); i > 0 {
		args = []string{"issue", "view", ref[i+1:], "-R", ref[:i], "--json", "number,title,url,state,body,labels,assignees,comments"}
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue view: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var is struct {
		Number    int
		Title     string
		URL       string
		State     string
		Body      string
		Labels    []struct{ Name string }
		Assignees []struct{ Login string }
		Comments  []struct {
			Author    struct{ Login string }
			Body      string
			CreatedAt time.Time
		}
	}
	if err := json.Unmarshal(b, &is); err != nil {
		return nil, err
	}
	t := &Ticket{Ref: fmt.Sprintf("#%d", is.Number), Title: is.Title, URL: is.URL, State: is.State, Description: is.Body}
	for _, l := range is.Labels {
		t.Labels = append(t.Labels, l.Name)
	}
	if len(is.Assignees) > 0 {
		t.Assignee = is.Assignees[0].Login
	}
	for _, c := range is.Comments {
		t.Comments = append(t.Comments, Comment{Author: c.Author.Login, Created: c.CreatedAt, Body: c.Body})
	}
	return t, nil
}

// File reads a local markdown file as the ticket: the first heading is the title.
type File struct{}

func (File) Fetch(_ context.Context, ref string) (*Ticket, error) {
	b, err := os.ReadFile(ref)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("ticket file %s not found", ref)
	}
	if err != nil {
		return nil, err
	}
	text := string(b)
	title := ref
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			break
		}
	}
	return &Ticket{Ref: ref, Title: title, Description: text}, nil
}

func sortComments(cs []Comment) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].Created.Before(cs[j-1].Created); j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

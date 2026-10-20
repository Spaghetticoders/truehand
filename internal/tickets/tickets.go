// Package tickets fetches a ticket with its comments from a tracker and renders it as the
// context an agent plans from.
package tickets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

type Comment struct {
	Author  string    `json:"author"`
	Created time.Time `json:"created"`
	Body    string    `json:"body"`
}

type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type Ticket struct {
	Ref         string    `json:"ref"`
	Title       string    `json:"title"`
	URL         string    `json:"url,omitempty"`
	State       string    `json:"state,omitempty"`
	Assignee    string    `json:"assignee,omitempty"`
	Labels      []string  `json:"labels,omitempty"`
	Description string    `json:"description"`
	Comments    []Comment `json:"comments,omitempty"`
	Links       []Link    `json:"links,omitempty"`
	Related     []Link    `json:"related,omitempty"`
}

type Adapter interface {
	Fetch(ctx context.Context, ref string) (*Ticket, error)
}

// ForName returns the adapter configured in tickets.adapter.
func ForName(name string) (Adapter, error) {
	switch name {
	case "linear":
		key := os.Getenv("LINEAR_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("LINEAR_API_KEY is not set (Linear → Settings → API → Personal API keys)")
		}
		return &Linear{Key: key}, nil
	case "github":
		return &GitHub{}, nil
	case "file", "":
		return &File{}, nil
	}
	return nil, fmt.Errorf("unknown tickets adapter %q (known: linear, github, file)", name)
}

// ParseRef splits "linear:SPA-1" into adapter and id; a bare id uses the default adapter.
func ParseRef(ref, def string) (adapter, id string) {
	if i := strings.Index(ref, ":"); i > 0 && !strings.Contains(ref[:i], "/") && !strings.HasPrefix(ref, "http") {
		return ref[:i], ref[i+1:]
	}
	return def, ref
}

// Markdown renders the ticket as context.md. Comments come in full: they carry the
// course corrections the description lacks.
func Markdown(t *Ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", t.Ref, t.Title)
	var meta []string
	if t.State != "" {
		meta = append(meta, "State: "+t.State)
	}
	if t.Assignee != "" {
		meta = append(meta, "Assignee: "+t.Assignee)
	}
	if len(t.Labels) > 0 {
		meta = append(meta, "Labels: "+strings.Join(t.Labels, ", "))
	}
	if t.URL != "" {
		meta = append(meta, "URL: "+t.URL)
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, " · ") + "\n\n")
	}
	b.WriteString("## Description\n\n")
	if strings.TrimSpace(t.Description) == "" {
		b.WriteString("_(empty)_\n\n")
	} else {
		b.WriteString(strings.TrimSpace(t.Description) + "\n\n")
	}
	fmt.Fprintf(&b, "## Comments (%d)\n\n", len(t.Comments))
	if len(t.Comments) == 0 {
		b.WriteString("_(none)_\n\n")
	}
	for _, c := range t.Comments {
		fmt.Fprintf(&b, "### %s — %s\n\n%s\n\n", c.Author, c.Created.Local().Format("2006-01-02 15:04"), strings.TrimSpace(c.Body))
	}
	if len(t.Links) > 0 {
		b.WriteString("## Attachments and links\n\n")
		for _, l := range t.Links {
			fmt.Fprintf(&b, "- [%s](%s)\n", l.Title, l.URL)
		}
		b.WriteString("\n")
	}
	if len(t.Related) > 0 {
		b.WriteString("## Related tickets\n\n")
		for _, l := range t.Related {
			fmt.Fprintf(&b, "- %s\n", l.Title)
		}
		b.WriteString("\n")
	}
	return b.String()
}

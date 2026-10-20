// Package figma is a minimal Figma REST client: node geometry, texts and renders.
package figma

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

var ErrTokenExpired = errors.New("figma: the personal access token has expired: generate a new one (Figma → Settings → Security) with file_content:read")

type Box struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Node struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Type  string      `json:"type"`
	Box   Box         `json:"box"`
	Texts []TextLayer `json:"texts"`
}

// TextLayer is a visible text with its box relative to the node's top-left corner.
type TextLayer struct {
	Text string `json:"text"`
	Box  Box    `json:"box"`
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return "https://api.figma.com"
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-Figma-Token", c.Token)
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("figma: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 4 {
			wait := time.Duration(5*(attempt+1)) * time.Second
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if resp.StatusCode != http.StatusOK {
			var e struct {
				Err     string `json:"err"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(body, &e)
			msg := e.Err + e.Message
			if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) && strings.Contains(strings.ToLower(msg), "expired") {
				return ErrTokenExpired
			}
			return fmt.Errorf("figma: GET %s: HTTP %d %s", path, resp.StatusCode, msg)
		}
		return json.Unmarshal(body, out)
	}
}

// Me validates the token in one call: an expired token fails every endpoint the same way.
func (c *Client) Me(ctx context.Context) error {
	var out map[string]any
	return c.get(ctx, "/v1/me", &out)
}

type rawNode struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Type                string    `json:"type"`
	Visible             *bool     `json:"visible"`
	Characters          string    `json:"characters"`
	AbsoluteBoundingBox *Box      `json:"absoluteBoundingBox"`
	Children            []rawNode `json:"children"`
}

// NormalizeID accepts ids from URLs ("12-34") and from the API ("12:34").
func NormalizeID(id string) string { return strings.ReplaceAll(id, "-", ":") }

// Node fetches a node's geometry and the characters of every visible text layer inside it.
func (c *Client) Node(ctx context.Context, fileKey, id string) (*Node, error) {
	id = NormalizeID(id)
	var out struct {
		Nodes map[string]*struct {
			Document rawNode `json:"document"`
		} `json:"nodes"`
	}
	if err := c.get(ctx, "/v1/files/"+url.PathEscape(fileKey)+"/nodes?ids="+url.QueryEscape(id), &out); err != nil {
		return nil, err
	}
	n := out.Nodes[id]
	if n == nil {
		return nil, fmt.Errorf("figma: node %s not found in file %s (a project can have several Figma files: check eyes.figma.file_key)", id, fileKey)
	}
	d := n.Document
	node := &Node{ID: d.ID, Name: d.Name, Type: d.Type}
	if d.AbsoluteBoundingBox != nil {
		node.Box = *d.AbsoluteBoundingBox
	}
	var walk func(r rawNode)
	walk = func(r rawNode) {
		if r.Visible != nil && !*r.Visible {
			return
		}
		if r.Type == "TEXT" && strings.TrimSpace(r.Characters) != "" {
			tl := TextLayer{Text: r.Characters}
			if r.AbsoluteBoundingBox != nil {
				tl.Box = *r.AbsoluteBoundingBox
				tl.Box.X -= node.Box.X
				tl.Box.Y -= node.Box.Y
			}
			node.Texts = append(node.Texts, tl)
		}
		for _, ch := range r.Children {
			walk(ch)
		}
	}
	walk(d)
	return node, nil
}

// Render exports a node as PNG at the given scale (Figma allows 0.01–4).
func (c *Client) Render(ctx context.Context, fileKey, id string, scale float64) ([]byte, error) {
	id = NormalizeID(id)
	var out struct {
		Err    string            `json:"err"`
		Images map[string]string `json:"images"`
	}
	q := fmt.Sprintf("/v1/images/%s?ids=%s&format=png&scale=%g", url.PathEscape(fileKey), url.QueryEscape(id), scale)
	if err := c.get(ctx, q, &out); err != nil {
		return nil, err
	}
	u := out.Images[id]
	if u == "" {
		return nil, fmt.Errorf("figma: no render for node %s %s", id, out.Err)
	}
	// The render URL is short-lived: download immediately.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("figma: render download HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

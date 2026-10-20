// Package config loads .truehand/config.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	Dir  = ".truehand"
	File = "config.yaml"
)

type Config struct {
	Version  int               `yaml:"version"`
	Base     string            `yaml:"base,omitempty"`
	Commands map[string]string `yaml:"commands,omitempty"`
	Tasks    Tasks             `yaml:"tasks,omitempty"`
	Tickets  Adapter           `yaml:"tickets,omitempty"`
	Eyes     Eyes              `yaml:"eyes,omitempty"`
	Tests    Tests             `yaml:"tests,omitempty"`
	Locks    Locks             `yaml:"locks,omitempty"`
	Gates    map[string][]Rule `yaml:"gates"`
}

// Tests tells hands which files are tests (everything else in the diff is "the fix").
type Tests struct {
	Globs []string `yaml:"globs,omitempty"`
	// AcceptBuildFailure lets test-proof count "does not compile without the change" as a
	// failing test. Weaker: a test with no assertion passes the proof.
	AcceptBuildFailure bool `yaml:"accept_build_failure,omitempty"`
}

// Locks configures what agent hooks enforce.
type Locks struct {
	// CommitFrom is the earliest state in which an agent may commit (default: reviewed).
	CommitFrom string `yaml:"commit_from,omitempty"`
	// AgentPush allows agents to git push (default: false).
	AgentPush bool `yaml:"agent_push,omitempty"`
}

var defaultTestGlobs = []string{
	"test/**", "tests/**", "integration_test/**", "**/__tests__/**",
	"**/*_test.*", "**/*.test.*", "**/*.spec.*", "**/test_*.py",
}

func (c *Config) TestGlobs() []string {
	if len(c.Tests.Globs) > 0 {
		return c.Tests.Globs
	}
	return defaultTestGlobs
}

func (c *Config) CommitFrom() string {
	if c.Locks.CommitFrom != "" {
		return c.Locks.CommitFrom
	}
	return "reviewed"
}

type Tasks struct {
	// Prefix of the ids Truehand assigns when a task is created without one.
	Prefix string `yaml:"prefix,omitempty"`
}

func (c *Config) TaskPrefix() string {
	if c.Tasks.Prefix != "" {
		return c.Tasks.Prefix
	}
	return "T"
}

type Adapter struct {
	Adapter string `yaml:"adapter,omitempty"`
}

type Eyes struct {
	Adapter string `yaml:"adapter,omitempty"`
	Design  string `yaml:"design,omitempty"`
	App     string `yaml:"app,omitempty"`
	Device  string `yaml:"device,omitempty"`
	// Launch relaunches the app before every capture (terminates the running process).
	Launch bool  `yaml:"launch,omitempty"`
	Figma  Figma `yaml:"figma,omitempty"`
	// Mask lists regions ignored by the pixel diff, in logical points. Unset means the
	// status bar (top 54pt); set it to [] to compare everything.
	Mask          []Rect            `yaml:"mask,omitempty"`
	MaxDeltaRatio float64           `yaml:"max_delta_ratio,omitempty"`
	Threshold     int               `yaml:"threshold,omitempty"`
	Screens       map[string]Screen `yaml:"screens,omitempty"`
}

type Screen struct {
	Node          string   `yaml:"node"`
	FileKey       string   `yaml:"file_key,omitempty"`
	Route         []Step   `yaml:"route,omitempty"`
	MaxDeltaRatio float64  `yaml:"max_delta_ratio,omitempty"`
	Mask          []Rect   `yaml:"mask,omitempty"`
	IgnoreTexts   []string `yaml:"ignore_texts,omitempty"`
	// SkipTexts disables the copy check (screens drawn on a canvas expose no labels).
	SkipTexts bool `yaml:"skip_texts,omitempty"`
}

type Rect struct {
	X float64 `yaml:"x" json:"x"`
	Y float64 `yaml:"y" json:"y"`
	W float64 `yaml:"w" json:"w"`
	H float64 `yaml:"h" json:"h"`
}

// Step mirrors eyes.Step; kept here so config has no dependency on adapters.
type Step struct {
	Tap   string `yaml:"tap,omitempty"`
	Wait  string `yaml:"wait,omitempty"`
	Sleep string `yaml:"sleep,omitempty"`
	Swipe string `yaml:"swipe,omitempty"`
	Text  string `yaml:"text,omitempty"`
	URL   string `yaml:"url,omitempty"`
	Click string `yaml:"click,omitempty"`
}

type Figma struct {
	FileKey  string `yaml:"file_key,omitempty"`
	TokenEnv string `yaml:"token_env,omitempty"`
}

// Rule is one requirement of a gate. With is check-specific configuration.
type Rule struct {
	Check string         `yaml:"check"`
	When  *When          `yaml:"when,omitempty"`
	With  map[string]any `yaml:"with,omitempty"`
}

// When restricts a rule to tasks whose diff touches at least one of the globs.
type When struct {
	Changed []string `yaml:"changed"`
}

func Path(root string) string {
	return filepath.Join(root, Dir, File)
}

func Load(root string) (*Config, error) {
	b, err := os.ReadFile(Path(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no %s found: run `truehand init` first", filepath.Join(Dir, File))
	}
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	// A typo such as "gate:" or "whne:" must not silently drop a rule.
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %w", err)
	}
	if c.Gates == nil {
		return nil, errors.New("config: no `gates:` section (write `gates: {}` to run without gates)")
	}
	if c.Version != 0 {
		return nil, fmt.Errorf("config: unsupported version %d", c.Version)
	}
	for state, rules := range c.Gates {
		for i, r := range rules {
			if r.Check == "" {
				return nil, fmt.Errorf("config: gates.%s[%d]: missing check", state, i)
			}
		}
	}
	return &c, nil
}

func (c *Config) Command(name string) string {
	return c.Commands[name]
}

// TestProofCommand is the command test-proof runs: commands.test_proof, else commands.test.
func (c *Config) TestProofCommand() string {
	if t := c.Command("test_proof"); t != "" {
		return t
	}
	return c.Command("test")
}

// EyesAdapter is the configured eyes adapter, ios-simulator by default.
func (c *Config) EyesAdapter() string {
	if c.Eyes.Adapter == "" {
		return "ios-simulator"
	}
	return c.Eyes.Adapter
}

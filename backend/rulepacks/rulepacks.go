// Package rulepacks embeds the versioned, community-editable country rule packs.
package rulepacks

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var files embed.FS

// Check is a declarative rule condition.
type Check struct {
	Field string `yaml:"field" json:"field"`
	Op    string `yaml:"op" json:"op"`
	Value any    `yaml:"value" json:"value"`
}

// Rule is one requirement with its public source. A rule that cannot yet be attributed to
// a public source has an empty Source and a Todo starting with "TODO(spec)"; such rules are
// informational and never evaluated.
type Rule struct {
	ID          string `yaml:"id" json:"id"`
	Description string `yaml:"description" json:"description"`
	Source      string `yaml:"source" json:"source"`
	Todo        string `yaml:"todo" json:"todo,omitempty"`
	AppliesTo   string `yaml:"appliesTo" json:"appliesTo"`
	Check       *Check `yaml:"check" json:"check,omitempty"`
	Severity    string `yaml:"severity" json:"severity"`
}

// Source cites a public document.
type Source struct {
	Title string `yaml:"title" json:"title"`
	URL   string `yaml:"url" json:"url"`
	Note  string `yaml:"note" json:"note,omitempty"`
}

// Pack is a country/market rule pack.
type Pack struct {
	ID             string   `yaml:"id" json:"id"`
	Name           string   `yaml:"name" json:"name"`
	Version        string   `yaml:"version" json:"version"`
	Market         string   `yaml:"market" json:"market"`
	Vertical       string   `yaml:"vertical" json:"vertical"`
	Sources        []Source `yaml:"sources" json:"sources"`
	Rules          []Rule   `yaml:"rules" json:"rules"`
	RetentionYears int      `yaml:"retentionYears" json:"retentionYears,omitempty"`
	Reporting      struct {
		Connector string   `yaml:"connector" json:"connector"`
		Triggers  []string `yaml:"triggers" json:"triggers"`
	} `yaml:"reporting" json:"reporting"`
}

// Load parses every embedded pack.
func Load() ([]Pack, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []Pack
	for _, e := range entries {
		b, err := files.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		var p Pack
		if err := yaml.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		for _, r := range p.Rules {
			if err := r.validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", e.Name(), err)
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r Rule) validate() error {
	switch {
	case r.Source == "" && !strings.HasPrefix(r.Todo, "TODO(spec)"):
		return fmt.Errorf("rule %s has no source (mark it todo: \"TODO(spec): ...\" if none is public yet)", r.ID)
	case r.Source == "" && r.Check != nil:
		return fmt.Errorf("rule %s has a check but no source: unattributed rules are never enforced", r.ID)
	}
	return nil
}

// Enforced reports whether the rule may be evaluated (it cites a public source).
func (r Rule) Enforced() bool { return r.Source != "" }

// Eval applies a check to facts; it returns pass and whether the check applied.
func Eval(c *Check, facts map[string]any) (bool, bool) {
	if c == nil {
		return true, false
	}
	v, ok := facts[c.Field]
	if !ok {
		return true, false
	}
	switch c.Op {
	case "eq":
		return fmt.Sprint(v) == fmt.Sprint(c.Value), true
	case "lte", "gte":
		a, aok := num(v)
		b, bok := num(c.Value)
		if !aok || !bok {
			return false, true
		}
		if c.Op == "lte" {
			return a <= b, true
		}
		return a >= b, true
	}
	return false, true
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

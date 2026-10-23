package app_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAsyncAPICoversPublishedTopics fails when code publishes a bus topic that
// asyncapi.yaml does not document (Kafka topic = "openspoor." + topic).
func TestAsyncAPICoversPublishedTopics(t *testing.T) {
	b, err := os.ReadFile("asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Channels map[string]struct {
			Address string `yaml:"address"`
		} `yaml:"channels"`
	}
	if err := yaml.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, c := range spec.Channels {
		documented[strings.TrimPrefix(c.Address, "openspoor.")] = true
	}

	publish := regexp.MustCompile(`\.Publish\(ctx, ("([a-z_.]+)"|([A-Za-z.]+)),`)
	constDecl := regexp.MustCompile(`(\w+)\s*=\s*"([a-z_.]+)"`)
	consts := map[string]string{}
	var calls [][]string
	err = filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range constDecl.FindAllStringSubmatch(string(src), -1) {
			consts[m[1]] = m[2]
		}
		calls = append(calls, publish.FindAllStringSubmatch(string(src), -1)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("found no Publish calls; the scan is broken")
	}
	for _, c := range calls {
		topic := c[2]
		if topic == "" {
			name := c[3][strings.LastIndex(c[3], ".")+1:]
			topic = consts[name]
		}
		if topic == "" {
			t.Errorf("cannot resolve topic %q", c[1])
			continue
		}
		if !documented[topic] {
			t.Errorf("topic %q is published but not documented in asyncapi.yaml", topic)
		}
	}
}

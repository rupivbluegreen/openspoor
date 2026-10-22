// Command validationpack turns `go test -json` output into an Operational Qualification
// report traced to the URS: go test -json ./... | validationpack -trace ../docs/validation/traceability.yaml > OQ-report.md
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type event struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

type result struct {
	status  string
	elapsed float64
}

func main() {
	tracePath := flag.String("trace", "../docs/validation/traceability.yaml", "requirement -> tests mapping")
	module := flag.String("module", "github.com/openspoor/openspoor/", "module prefix to strip from package names")
	flag.Parse()

	results := map[string]result{}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Test == "" || strings.Contains(e.Test, "/") {
			continue
		}
		switch e.Action {
		case "pass", "fail", "skip":
			results[strings.TrimPrefix(e.Package, *module)+"/"+e.Test] = result{e.Action, e.Elapsed}
		}
	}
	trace := map[string][]string{}
	if b, err := os.ReadFile(*tracePath); err == nil {
		_ = yaml.Unmarshal(b, &trace)
	}
	commit, _ := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	var pass, fail, skip int
	names := make([]string, 0, len(results))
	for k, r := range results {
		names = append(names, k)
		switch r.status {
		case "pass":
			pass++
		case "fail":
			fail++
		default:
			skip++
		}
	}
	sort.Strings(names)
	w := bufio.NewWriter(os.Stdout)
	defer func() {
		if err := w.Flush(); err != nil {
			fmt.Fprintln(os.Stderr, "write report:", err)
			os.Exit(1)
		}
	}()
	fmt.Fprintf(w, "# Operational Qualification report — Open Spoor\n\n")
	fmt.Fprintf(w, "- Generated: %s\n- Commit: `%s`\n- Platform: %s/%s, %d CPUs, %s\n- Tests: **%d passed, %d failed, %d skipped**\n\n",
		time.Now().UTC().Format(time.RFC3339), strings.TrimSpace(string(commit)), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version(), pass, fail, skip)
	fmt.Fprintf(w, "## Requirements traceability\n\n| Requirement | Tests | Result |\n|---|---|---|\n")
	reqs := make([]string, 0, len(trace))
	for k := range trace {
		reqs = append(reqs, k)
	}
	sort.Strings(reqs)
	for _, req := range reqs {
		verdict := "verified"
		var cells []string
		for _, t := range trace[req] {
			r, ok := results[t]
			switch {
			case !ok:
				cells = append(cells, t+" (not run)")
				verdict = "**not verified**"
			case r.status != "pass":
				cells = append(cells, t+" ("+r.status+")")
				verdict = "**not verified**"
			default:
				cells = append(cells, t)
			}
		}
		fmt.Fprintf(w, "| %s | %s | %s |\n", req, strings.Join(cells, "<br>"), verdict)
	}
	fmt.Fprintf(w, "\n## Executed tests\n\n| Test | Result | Duration (s) |\n|---|---|---|\n")
	for _, n := range names {
		r := results[n]
		fmt.Fprintf(w, "| %s | %s | %.2f |\n", n, r.status, r.elapsed)
	}
	fmt.Fprintf(w, "\n## Approval\n\n| Role | Name | Signature | Date |\n|---|---|---|---|\n| Executed by | | | |\n| Reviewed by (QA) | | | |\n")
	if fail > 0 {
		os.Exit(1)
	}
}

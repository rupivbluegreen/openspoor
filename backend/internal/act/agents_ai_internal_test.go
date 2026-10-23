package act

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsBlockedIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "127.5.5.5": true, "::1": true, "10.1.2.3": true, "172.16.0.1": true, "172.31.255.255": true, "192.168.1.1": true,
		"169.254.169.254": true, "169.254.0.1": true, "fe80::1": true, "fc00::1": true, "fd12:3456::1": true, "0.0.0.0": true, "::": true,
		"100.64.0.1": true, "224.0.0.1": true, "ff02::1": true, "198.18.0.1": true, "240.0.0.1": true, "::ffff:127.0.0.1": true, "::ffff:10.0.0.1": true,
		"8.8.8.8": false, "1.1.1.1": false, "93.184.216.34": false, "2606:4700:4700::1111": false, "172.32.0.1": false, "::ffff:8.8.8.8": false,
	} {
		if got := IsBlockedIP(net.ParseIP(ip)); got != want {
			t.Errorf("IsBlockedIP(%s) = %v, want %v", ip, got, want)
		}
	}
	if !IsBlockedIP(nil) || !IsBlockedIP(net.IP{1, 2}) {
		t.Error("an unparseable address must be blocked")
	}
}

func TestValidateWatchURL(t *testing.T) {
	ok := []string{"https://www.fda.gov/page", "https://eur-lex.europa.eu:443/x?y=1", "https://8.8.8.8/"}
	for _, u := range ok {
		if _, err := ValidateWatchURL(u, false); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	bad := []string{"", "not a url", "/relative", "http://www.fda.gov/", "ftp://x.test/", "file:///etc/passwd", "https://user:pw@www.fda.gov/", "https://www.fda.gov:8443/",
		"https://127.0.0.1/", "https://[::1]/", "https://10.0.0.5/", "https://169.254.169.254/latest/meta-data", "https://192.168.0.1/", "https://[fe80::1]/", "https:///path"}
	for _, u := range bad {
		if _, err := ValidateWatchURL(u, false); err == nil {
			t.Errorf("%q must be rejected", u)
		}
	}
	// The test-only flag allows plain http, other ports and loopback, but still not private ranges.
	for _, u := range []string{"http://127.0.0.1:8080/x", "https://localhost:9443/"} {
		if _, err := ValidateWatchURL(u, true); err != nil {
			t.Errorf("insecure %s: %v", u, err)
		}
	}
	for _, u := range []string{"http://10.0.0.1/", "http://169.254.169.254/", "ftp://127.0.0.1/"} {
		if _, err := ValidateWatchURL(u, true); err == nil {
			t.Errorf("insecure %s must still be rejected", u)
		}
	}
}

func TestFetchRefusesNonPublicAddresses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A hostname that resolves to loopback passes URL validation; the dial check stops it.
	_, _, err := fetchWatched(ctx, newWatchClient(false), "https://localhost/", false)
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("localhost: %v", err)
	}
	// A redirect to a private address is refused before it is followed.
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1/secret", http.StatusFound)
	}))
	defer redir.Close()
	if _, _, err := fetchWatched(ctx, newWatchClient(true), redir.URL, true); err == nil {
		t.Fatal("redirect to a private address was followed")
	}
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.URL.Path+"x", http.StatusFound) }))
	defer loop.Close()
	if _, _, err := fetchWatched(ctx, newWatchClient(true), loop.URL, true); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect loop: %v", err)
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", maxWatchBytes+10)))
	}))
	defer big.Close()
	if _, _, err := fetchWatched(ctx, newWatchClient(true), big.URL, true); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("size limit: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusTeapot) }))
	defer bad.Close()
	if _, _, err := fetchWatched(ctx, newWatchClient(true), bad.URL, true); err == nil || !strings.Contains(err.Error(), "HTTP 418") {
		t.Fatalf("status: %v", err)
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<p>hi</p>"))
	}))
	defer ok.Close()
	body, mt, err := fetchWatched(ctx, newWatchClient(true), ok.URL, true)
	if err != nil || string(body) != "<p>hi</p>" || mt != "text/html" {
		t.Fatalf("fetch: %q %q %v", body, mt, err)
	}
	if _, _, err := fetchWatched(ctx, newWatchClient(true), "::bad", true); err == nil {
		t.Fatal("bad url")
	}
}

func TestNormalizeText(t *testing.T) {
	page := `<html><head><title>T</title><style>p{color:red}</style><script>var x = "<p>no</p>";</script></head>
	<body><!-- a comment --><nav>Menu</nav><div id="content" class="main wide"><h1>Rule  A</h1><p>Text&nbsp;one &amp; two</p><ul><li>Item 1</li><li>Item 2</li></ul>
	<div class="note">Inner</div></div><footer>Footer</footer></body></html>`
	all, err := normalizeText([]byte(page), "text/html", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Menu", "Rule A", "Text one & two", "Item 1", "Item 2", "Inner", "Footer", "T"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %q", want, all)
		}
	}
	for _, not := range []string{"color:red", "var x", "comment", "<"} {
		if strings.Contains(all, not) {
			t.Errorf("%q must be removed from %q", not, all)
		}
	}
	for sel, want := range map[string]string{"#content": "Rule A\nText one & two\nItem 1\nItem 2\nInner", ".main": "Rule A\nText one & two\nItem 1\nItem 2\nInner", "div#content": "Rule A\nText one & two\nItem 1\nItem 2\nInner",
		".note": "Inner", "div.note": "Inner", "ul": "Item 1\nItem 2", "h1": "Rule A", "nav": "Menu"} {
		got, err := normalizeText([]byte(page), "text/html", sel)
		if err != nil || got != want {
			t.Errorf("selector %s: %q %v, want %q", sel, got, err, want)
		}
	}
	for _, sel := range []string{"#missing", ".none", "table", "p#content"} {
		if _, err := normalizeText([]byte(page), "text/html", sel); err == nil || !strings.Contains(err.Error(), "matched nothing") {
			t.Errorf("selector %s should match nothing: %v", sel, err)
		}
	}
	if _, err := extractSelector(page, "a b"); err == nil {
		t.Error("descendant selectors are not supported")
	}
	// Unclosed elements take the rest; self-closing and nested same-name tags balance.
	if got, _ := extractSelector(`<div id="a"><div>x</div>y</div>z`, "#a"); got != "<div>x</div>y" {
		t.Errorf("nested: %q", got)
	}
	if got, _ := extractSelector(`<div id="a">open`, "#a"); got != "open" {
		t.Errorf("unclosed: %q", got)
	}
	// Plain text, JSON, binary.
	txt, _ := normalizeText([]byte("  a   b \r\n\r\n c\t\n"), "text/plain", "")
	if txt != "a b\nc" {
		t.Errorf("text: %q", txt)
	}
	js, _ := normalizeText([]byte(`{"a": 1}`), "application/json", "")
	if js != `{"a": 1}` {
		t.Errorf("json: %q", js)
	}
	b1, _ := normalizeText([]byte("%PDF-1"), "application/pdf", "")
	b2, _ := normalizeText([]byte("%PDF-2"), "application/pdf", "")
	if b1 == b2 || !strings.Contains(b1, "binary document (application/pdf), 6 bytes") {
		t.Errorf("binary: %q %q", b1, b2)
	}
	if hashText("a") == hashText("b") || len(hashText("a")) != 64 {
		t.Error("hash")
	}
	for _, ok := range []string{"", "div", "#id", ".cls", "div#id", "div.cls", "h1"} {
		if !ValidSelector(ok) {
			t.Errorf("selector %q should be valid", ok)
		}
	}
	for _, bad := range []string{"a b", "a>b", "#", "1a", "div[x]", "*"} {
		if ValidSelector(bad) {
			t.Errorf("selector %q should be invalid", bad)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	if d := unifiedDiff("a\nb", "a\nb"); d != "" {
		t.Fatalf("equal texts: %q", d)
	}
	old := "l1\nl2\nl3\nl4\nOLD\nl6\nl7\nl8\nl9"
	cur := "l1\nl2\nl3\nl4\nNEW\nADDED\nl6\nl7\nl8\nl9"
	d := unifiedDiff(old, cur)
	for _, want := range []string{"--- previous", "+++ current", "@@ -2,7 +2,8 @@", " l2\n", "-OLD\n", "+NEW\n", "+ADDED\n", " l8\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("missing %q in\n%s", want, d)
		}
	}
	if strings.Contains(d, "l1\n") && !strings.Contains(d, " l2") {
		t.Errorf("context:\n%s", d)
	}
	// Whole-text replacement and long changes are cut.
	var many []string
	for i := 0; i < 200; i++ {
		many = append(many, "line")
	}
	long := unifiedDiff("x", strings.Join(many, "\n"))
	if !strings.Contains(long, "more lines") || strings.Count(long, "\n") > 2*diffMaxLines+10 {
		t.Errorf("long diff not cut: %d lines", strings.Count(long, "\n"))
	}
	if d := unifiedDiff("a", "a\nb"); !strings.Contains(d, "+b") {
		t.Errorf("append: %s", d)
	}
	if d := unifiedDiff("a\nb", "a"); !strings.Contains(d, "-b") {
		t.Errorf("remove: %s", d)
	}
}

func TestHelpers(t *testing.T) {
	if f, o := detectFormat("\xef\xbb\xbf<?xml version='1.0'?><a/>"); f != "xml" || o != nil {
		t.Errorf("xml: %s", f)
	}
	if f, _ := detectFormat("ISA*00*"); f != "edi" {
		t.Errorf("edi: %s", f)
	}
	if f, _ := detectFormat(`{"a":1}`); f != "json" {
		t.Errorf("json: %s", f)
	}
	if f, _ := detectFormat(`{"a":`); f == "json" {
		t.Errorf("broken json detected as json")
	}
	if f, o := detectFormat("a\tb\tc\n1\t2\t3"); f != "csv" || string(o) != `{"delimiter":"tab"}` {
		t.Errorf("tab: %s %s", f, o)
	}
	if f, o := detectFormat("a;b;c,d\n1;2;3,4"); f != "csv" || string(o) != `{"delimiter":";"}` {
		t.Errorf("semicolon: %s %s", f, o)
	}
	if f, _ := detectFormat("plain words"); f != "unknown" {
		t.Errorf("unknown: %s", f)
	}
	if got := sampleValues([]map[string]string{{"a": " "}, {"a": "x"}, {"a": "x"}, {"a": "y"}, {"a": "z"}, {"a": "w"}}, "a"); got != `"x", "y", "z"` {
		t.Errorf("sample values: %s", got)
	}
	if sampleValues(nil, "a") != "(empty)" {
		t.Error("empty sample values")
	}
	if clip("héllo", 2) != "h" || clip("abc", 5) != "abc" {
		t.Error("clip must not split a rune")
	}
	if dur(3*time.Hour) != "3 h" || dur(-72*time.Hour) != "3 days" {
		t.Error("dur")
	}
	if failureDetail(map[string]any{"message": strings.Repeat("x", 300)}) != ": "+strings.Repeat("x", 200)+"..." || failureDetail(nil) != "" {
		t.Error("failureDetail")
	}
	if channelName("") != "configured" || channelName("as2") != "as2" {
		t.Error("channelName")
	}
	ok, latest := itemSatisfied(obItem{kind: "test", channel: "as2"}, []obTest{{channel: "https", passed: true}, {channel: "as2", passed: false}})
	if ok || latest == nil || latest.passed {
		t.Error("test item satisfied by a run on another channel")
	}
	if ok, _ := itemSatisfied(obItem{kind: "test"}, []obTest{{channel: "https", passed: true}}); !ok {
		t.Error("any-channel test item")
	}
	if ok, _ := itemSatisfied(obItem{kind: "manual", done: true}, nil); !ok {
		t.Error("manual item")
	}
	if !containsWord("a b  c", "b") || containsWord("ab", "b") {
		t.Error("containsWord")
	}
}

type fakeTool struct {
	risk string
	rev  bool
}

func (fakeTool) Name() string       { return "x" }
func (fakeTool) Scope() []string    { return nil }
func (f fakeTool) Risk() string     { return f.risk }
func (f fakeTool) Reversible() bool { return f.rev }
func (fakeTool) Execute(context.Context, pgx.Tx, json.RawMessage) (any, any, error) {
	return nil, nil, nil
}
func (fakeTool) Revert(context.Context, pgx.Tx, json.RawMessage, json.RawMessage) error { return nil }

func TestEffectiveMode(t *testing.T) {
	low, high, irrev := fakeTool{"low", true}, fakeTool{"high", true}, fakeTool{"low", false}
	for _, c := range []struct {
		tool  fakeTool
		modes map[string]string
		agent bool
		want  string
	}{
		{low, nil, false, ModeApprove}, {low, nil, true, ModeAuto}, {high, nil, true, ModeApprove}, {irrev, nil, true, ModeApprove},
		{low, map[string]string{"x": ModeSuggest}, true, ModeSuggest}, {low, map[string]string{"x": ModeApprove}, true, ModeApprove},
		{low, map[string]string{"x": ModeAuto}, false, ModeAuto}, {high, map[string]string{"x": ModeAuto}, false, ModeApprove}, {irrev, map[string]string{"x": ModeAuto}, false, ModeApprove},
	} {
		if got := effectiveMode(c.tool, c.modes, c.agent); got != c.want {
			t.Errorf("%+v: got %s want %s", c, got, c.want)
		}
	}
}

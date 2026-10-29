// Command parity replays requests against the Vapor backend and v2 and
// reports every difference in status, selected headers, and JSON body.
//
//	go run ./cmd/parity -a http://localhost:8081 -b http://localhost:8090 -cases parity/cases.jsonl
//
// Each line of the cases file is one request:
//
//	{"name":"me","method":"GET","path":"/v1/users/me","as":"<user uuid>","body":{...}}
//
// "rawBody" (a string) is sent verbatim in place of "body", for non-JSON
// uploads.
//
// "as" signs a SessionToken for that user with -jwt-secret, so both backends
// see the same caller. Volatile fields (requestId, timestamps listed with
// -ignore) are dropped before comparing. Only use against databases you own:
// mutating cases run twice, once per backend.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/auth"

	"github.com/google/uuid"
)

type testCase struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	As      string            `json:"as"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
	// RawBody is sent verbatim instead of Body (CSV uploads); set its
	// Content-Type in Headers.
	RawBody *string `json:"rawBody"`
}

type result struct {
	status  int
	headers http.Header
	body    []byte
}

func main() {
	a := flag.String("a", "http://localhost:8081", "reference backend (Vapor)")
	b := flag.String("b", "http://localhost:8090", "candidate backend (v2)")
	casesPath := flag.String("cases", "", "JSONL file of cases (default: stdin)")
	secret := flag.String("jwt-secret", os.Getenv("JWT_SECRET"), "JWT_SECRET both backends use")
	ignore := flag.String("ignore", "requestId", "comma-separated JSON keys to ignore at any depth")
	headers := flag.String("headers", "Content-Type,X-Frame-Options,X-Content-Type-Options,Referrer-Policy", "headers to compare")
	flag.Parse()

	in := io.Reader(os.Stdin)
	if *casesPath != "" {
		raw, err := os.ReadFile(*casesPath)
		if err != nil {
			fail(err)
		}
		in = bytes.NewReader(raw)
	}
	ignored := map[string]bool{}
	for _, k := range strings.Split(*ignore, ",") {
		ignored[strings.TrimSpace(k)] = true
	}
	signer := auth.NewSigner(*secret)
	client := &http.Client{
		Timeout: 60 * time.Second,
		// Compare redirects themselves, not wherever they lead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	var total, failed int
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var tc testCase
		if err := json.Unmarshal([]byte(line), &tc); err != nil {
			fail(fmt.Errorf("bad case %q: %w", line, err))
		}
		total++
		ra, errA := do(client, *a, tc, signer)
		rb, errB := do(client, *b, tc, signer)
		if errA != nil || errB != nil {
			failed++
			fmt.Printf("FAIL %s: transport a=%v b=%v\n", tc.Name, errA, errB)
			continue
		}
		if diffs := compare(ra, rb, ignored, strings.Split(*headers, ",")); len(diffs) > 0 {
			failed++
			fmt.Printf("FAIL %s %s %s\n", tc.Name, tc.Method, tc.Path)
			for _, d := range diffs {
				fmt.Println("   ", d)
			}
		} else {
			fmt.Printf("ok   %s\n", tc.Name)
		}
	}
	if err := sc.Err(); err != nil {
		fail(err)
	}
	fmt.Printf("\n%d cases, %d differ\n", total, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func do(client *http.Client, base string, tc testCase, signer *auth.Signer) (*result, error) {
	method := tc.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	switch {
	case tc.RawBody != nil:
		body = strings.NewReader(*tc.RawBody)
	case len(tc.Body) > 0:
		body = bytes.NewReader(tc.Body)
	}
	req, err := http.NewRequest(method, base+tc.Path, body) //nolint:noctx // CLI tool, client has a timeout
	if err != nil {
		return nil, err
	}
	if len(tc.Body) > 0 && tc.RawBody == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range tc.Headers {
		req.Header.Set(k, v)
	}
	if h := tc.Headers["Host"]; h != "" {
		req.Host = h // net/http ignores a Host header set on req.Header
	}
	if tc.As != "" {
		id, err := uuid.Parse(tc.As)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		tok, err := signer.Mint(id, now.Add(time.Hour), now)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &result{status: resp.StatusCode, headers: resp.Header, body: data}, nil
}

func compare(a, b *result, ignored map[string]bool, headers []string) []string {
	var diffs []string
	if a.status != b.status {
		diffs = append(diffs, fmt.Sprintf("status: vapor=%d v2=%d", a.status, b.status))
	}
	for _, h := range headers {
		h = strings.TrimSpace(h)
		if va, vb := a.headers.Get(h), b.headers.Get(h); !strings.EqualFold(va, vb) {
			diffs = append(diffs, fmt.Sprintf("header %s: vapor=%q v2=%q", h, va, vb))
		}
	}
	var ja, jb any
	errA := json.Unmarshal(a.body, &ja)
	errB := json.Unmarshal(b.body, &jb)
	switch {
	case errA != nil || errB != nil:
		if !bytes.Equal(a.body, b.body) {
			diffs = append(diffs, fmt.Sprintf("body (non-JSON): vapor=%.200q v2=%.200q", a.body, b.body))
		}
	default:
		diffJSON("$", strip(ja, ignored), strip(jb, ignored), &diffs)
	}
	return diffs
}

func strip(v any, ignored map[string]bool) any {
	switch n := v.(type) {
	case map[string]any:
		for k := range n {
			if ignored[k] {
				delete(n, k)
			} else {
				n[k] = strip(n[k], ignored)
			}
		}
	case []any:
		for i := range n {
			n[i] = strip(n[i], ignored)
		}
	}
	return v
}

const maxDiffs = 20

func diffJSON(path string, a, b any, out *[]string) {
	if len(*out) >= maxDiffs {
		return
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: type vapor=object v2=%T", path, b))
			return
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			x, inA := av[k]
			y, inB := bv[k]
			switch {
			case !inA:
				*out = append(*out, fmt.Sprintf("%s.%s: only in v2 (%v)", path, k, short(y)))
			case !inB:
				*out = append(*out, fmt.Sprintf("%s.%s: missing in v2 (vapor=%v)", path, k, short(x)))
			default:
				diffJSON(path+"."+k, x, y, out)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: type vapor=array v2=%T", path, b))
			return
		}
		if len(av) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s: length vapor=%d v2=%d", path, len(av), len(bv)))
			return
		}
		for i := range av {
			diffJSON(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, fmt.Sprintf("%s: vapor=%v v2=%v", path, short(a), short(b)))
		}
	}
}

func short(v any) string {
	s := fmt.Sprint(v)
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "parity:", err)
	os.Exit(2)
}

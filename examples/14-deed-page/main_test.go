package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolName = "deed-page_publish_site"

// modelFixture speaks Chat Completions so the tests go through the real model
// adapter, capability selection and confirmation flow.
func modelFixture(t *testing.T, selected bool, published chan<- string) string {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		calls++
		message := map[string]any{"role": "assistant"}
		switch calls {
		case 1:
			if len(request.Tools) != 0 {
				t.Error("tools loaded before capability selection")
			}
			catalog, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(catalog), "deed-page") {
				t.Error("missing capability catalog")
			}
			message["content"] = `{"providers":[]}`
			if selected {
				message["content"] = `{"providers":["deed-page"]}`
			}
		case 2:
			if !selected {
				message["content"] = "Nothing to publish."
				break
			}
			if len(request.Tools) != 1 || request.Tools[0].Function.Name != toolName {
				t.Errorf("got tools %+v, want only %s", request.Tools, toolName)
			}
			arguments := `{"files":{"index.html":"<!doctype html><h1>zhizhi-agent-runtime example</h1>"},"ttl_seconds":600}`
			message["tool_calls"] = []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": toolName, "arguments": arguments}}}
		default:
			var result string
			for _, msg := range request.Messages {
				if msg.Role == "tool" {
					result = msg.Content
				}
			}
			if published != nil {
				published <- result
			}
			message["content"] = "Published: " + result
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

type requestRecorder struct {
	base    http.RoundTripper
	mu      sync.Mutex
	methods []string
}

func (r *requestRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/chat/completions" {
		return r.base.RoundTrip(req)
	}
	if req.Header.Get("Authorization") != "" {
		return nil, fmt.Errorf("unexpected MCP authorization")
	}
	if req.UserAgent() != "zhizhi-agent-runtime/"+zhizhi.Version {
		return nil, fmt.Errorf("unexpected MCP user-agent %q", req.UserAgent())
	}
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &rpc) == nil {
			method := rpc.Method
			if rpc.Params.Name != "" {
				method += ":" + rpc.Params.Name
			}
			r.mu.Lock()
			r.methods = append(r.methods, method)
			r.mu.Unlock()
		}
	}
	return r.base.RoundTrip(req)
}

func (r *requestRecorder) has(method string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.methods {
		if m == method {
			return true
		}
	}
	return false
}

func recordRequests(t *testing.T) *requestRecorder {
	t.Helper()
	original := http.DefaultTransport
	recorder := &requestRecorder{base: original}
	http.DefaultTransport = recorder
	t.Cleanup(func() { http.DefaultTransport = original })
	return recorder
}

func testEnvironment(modelURL, mcpURL string) func(string) string {
	env := map[string]string{"LLM_BASE_URL": modelURL, "LLM_MODEL": "fixture", "DEEDPAGE_MCP_URL": mcpURL}
	return func(key string) string { return env[key] }
}

func localMCP(t *testing.T) string {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "deed-page-fixture", Version: "1"}, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{"files": map[string]any{"type": "object"}, "ttl_seconds": map[string]any{"type": "integer"}, "slug": map[string]any{"type": "string"}}}
	for _, name := range []string{"publish_site", "delete_site"} {
		sdk.AddTool(server, &sdk.Tool{Name: name, Description: name, InputSchema: schema}, func(_ context.Context, req *sdk.CallToolRequest, input map[string]any) (*sdk.CallToolResult, any, error) {
			if req.Params.Name != "publish_site" {
				t.Errorf("excluded tool %s called", req.Params.Name)
			}
			if input["files"] == nil {
				t.Error("missing files")
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: `{"ok":true,"url":"https://quiet-field-a1b2.deed.page/","slug":"quiet-field-a1b2"}`}}}, nil, nil
		})
	}
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

func TestPublishAfterConfirmation(t *testing.T) {
	recorder := recordRequests(t)
	var output bytes.Buffer
	err := run(context.Background(), testEnvironment(modelFixture(t, true, nil), localMCP(t)), nil, strings.NewReader("y\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "PUBLIC page (expires in 600s): index.html") {
		t.Fatalf("missing confirmation prompt: %s", output.String())
	}
	if !strings.Contains(output.String(), "https://quiet-field-a1b2.deed.page/") {
		t.Fatalf("missing page URL: %s", output.String())
	}
	if !recorder.has("tools/call:publish_site") {
		t.Fatalf("publish_site not called: %v", recorder.methods)
	}
}

func TestDeclinedPublishDoesNotCallTool(t *testing.T) {
	recorder := recordRequests(t)
	var output bytes.Buffer
	err := run(context.Background(), testEnvironment(modelFixture(t, true, nil), localMCP(t)), nil, strings.NewReader("n\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Not published.") {
		t.Fatalf("missing decline message: %s", output.String())
	}
	if recorder.has("tools/call:publish_site") {
		t.Fatal("publish_site called without confirmation")
	}
}

func TestUnselectedCapabilityDoesNotConnect(t *testing.T) {
	recorder := recordRequests(t)
	var output bytes.Buffer
	if err := run(context.Background(), testEnvironment(modelFixture(t, false, nil), "http://127.0.0.1:1"), []string{"Hello"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if len(recorder.methods) != 0 {
		t.Fatalf("unselected MCP connected: %v", recorder.methods)
	}
}

func TestConfigurationRequired(t *testing.T) {
	if err := run(context.Background(), func(string) string { return "" }, nil, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("missing model configuration accepted")
	}
}

// TestLiveDeedPage publishes a short-lived public page to the real anonymous
// endpoint, checks it is served, then deletes it with the returned token.
func TestLiveDeedPage(t *testing.T) {
	if os.Getenv("ZHIZHI_DEED_PAGE_LIVE") != "1" {
		t.Skip("set ZHIZHI_DEED_PAGE_LIVE=1 to publish (and then delete) a temporary public page")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	published := make(chan string, 1)
	if err := run(ctx, testEnvironment(modelFixture(t, true, published), ""), nil, strings.NewReader("y\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	var site struct {
		URL   string `json:"url"`
		Slug  string `json:"slug"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(<-published), &site); err != nil || site.URL == "" || site.Token == "" {
		t.Fatalf("unexpected publish result (err %v)", err)
	}
	if status := httpStatus(t, site.URL); status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", site.URL, status)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "zhizhi-live-cleanup", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: deedPageEndpoint}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "delete_site", Arguments: map[string]any{"slug": site.Slug, "token": site.Token}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if status := httpStatus(t, site.URL); status != http.StatusNotFound {
		t.Fatalf("GET %s after delete = %d, want 404", site.URL, status)
	}
	t.Logf("published, served and deleted %s", site.URL)
}

func httpStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

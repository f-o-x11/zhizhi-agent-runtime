package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/cocoyes/zhizhi-agent-runtime/mcp"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

const deedPageEndpoint = "https://deed.page/mcp"
const defaultRequest = "Write a one-page HTML summary of what zhizhi-agent-runtime does and publish it as a temporary page that expires in one hour. Reply with the page URL."

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Getenv, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, args []string, in io.Reader, out io.Writer) error {
	if getenv("LLM_BASE_URL") == "" || getenv("LLM_MODEL") == "" {
		return fmt.Errorf("set LLM_BASE_URL and LLM_MODEL (and LLM_API_KEY if your model endpoint requires it)")
	}
	endpoint := getenv("DEEDPAGE_MCP_URL")
	if endpoint == "" {
		endpoint = deedPageEndpoint
	}
	// Publishing creates a public page, so the tool is a confirmed write.
	publish := mcp.ToolPolicy{SideEffect: tool.SideEffectWriteNonIdempotent, Idempotency: tool.IdempotencyNonIdempotent, Retryable: false, RiskLevel: tool.RiskMedium, Confirmation: tool.ConfirmationAlways}
	capabilities, err := mcp.NewCapabilityProvider(mcp.Config{
		ID: "deed-page", Name: "deed-page",
		Description: "Publish HTML or other static files as a public web page and get back its URL. Use only when the user asks to publish or share a page.",
		Endpoint:    endpoint, Transport: mcp.TransportStreamableHTTP,
		Headers:      map[string]string{"User-Agent": "zhizhi-agent-runtime/" + zhizhi.Version},
		AllowTools:   []string{"publish_site"},
		ToolPolicies: map[string]mcp.ToolPolicy{"publish_site": publish},
		Required:     true,
	})
	if err != nil {
		return err
	}
	defer capabilities.Close()
	modelClient := openaicompat.New(openaicompat.Config{
		BaseURL: getenv("LLM_BASE_URL"), APIKey: getenv("LLM_API_KEY"), Model: getenv("LLM_MODEL"),
		Thinking: getenv("LLM_THINKING"), ReasoningEffort: getenv("LLM_REASONING_EFFORT"),
	})
	agent, err := zhizhi.New(zhizhi.WithModel(modelClient), zhizhi.WithMCPCapabilityProvider(capabilities), zhizhi.WithConfirmation(askBeforePublish(in, out)))
	if err != nil {
		return err
	}
	defer agent.Close(context.Background())
	request := defaultRequest
	if len(args) > 0 {
		request = strings.Join(args, " ")
	}
	mode := zhizhi.RunModeAgentSimple
	response, err := agent.Run(ctx, zhizhi.Request{Input: request, Mode: &mode})
	if err != nil {
		return err
	}
	if response.Suspended {
		_, err = fmt.Fprintln(out, "Not published.")
		return err
	}
	_, err = fmt.Fprintln(out, response.Text)
	return err
}

// askBeforePublish shows which files would become public and waits for "y".
func askBeforePublish(in io.Reader, out io.Writer) zhizhi.ConfirmFunc {
	reader := bufio.NewReader(in)
	return func(_ context.Context, spec tool.Spec, raw json.RawMessage) (bool, error) {
		var args struct {
			Files      map[string]any `json:"files"`
			TTLSeconds int            `json:"ttl_seconds"`
		}
		_ = json.Unmarshal(raw, &args)
		names := make([]string, 0, len(args.Files))
		for name := range args.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		expiry := "no expiry"
		if args.TTLSeconds > 0 {
			expiry = fmt.Sprintf("expires in %ds", args.TTLSeconds)
		}
		fmt.Fprintf(out, "%s wants to publish a PUBLIC page (%s): %s\nPublish? [y/N] ", spec.ID, expiry, strings.Join(names, ", "))
		answer, err := reader.ReadString('\n')
		if err != nil && answer == "" {
			return false, nil
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		return answer == "y" || answer == "yes", nil
	}
}

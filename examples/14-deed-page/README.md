# deed.page publishing example

Let an agent publish a static page and reply with its URL, through the runtime's
lazy MCP capability selection and confirmation flow. This separate example
connects to the [deed.page](https://deed.page) MCP endpoint over Streamable HTTP.
The model first sees only the capability description; a selected server connects
and loads tools. The model can then call `publish_site`, and the example asks you
on the terminal before anything is published.

deed.page's MCP endpoint is free and needs no API key or account. Your model
endpoint still needs its own configuration and may charge for inference.

From the repository root, configure an OpenAI-compatible endpoint:

```bash
export LLM_BASE_URL="https://your-provider.example/v1"
export LLM_API_KEY="your-model-provider-key"
export LLM_MODEL="your-model"
go run ./examples/14-deed-page
```

Or describe your own page:

```bash
go run ./examples/14-deed-page "Make a small HTML changelog page for v1.0.9 and publish it for 10 minutes."
```

**Published pages are public.** Anyone with the URL can open them, so don't
publish secrets or private data. Only `publish_site` is exposed. Its policy is a
non-idempotent, medium-risk write with `ConfirmationAlways`, so every call goes
through `zhizhi.WithConfirmation`. The prompt lists the file names and expiry and
publishes only on `y`. Anything else suspends the run and prints `Not published.`

`publish_site` returns the page URL, its slug and a management token. The token
is part of the tool result, so the model sees it. Keep it if you want to update or
delete the page later. Pass `ttl_seconds` to make pages expire on their own.
See the [deed.page docs](https://deed.page/docs) for the full tool list and limits.

`DEEDPAGE_MCP_URL` optionally overrides `https://deed.page/mcp`, for example when
testing against a local MCP fixture. The example sends a
`zhizhi-agent-runtime/<version>` User-Agent and no credentials. It doesn't load
dotenv files or saved settings. A selected server's connection failure fails the
run. The run has a two-minute deadline and supports Ctrl+C cancellation.

Running this example is opt-in; it doesn't change runtime defaults or other
examples.

## Validation

```bash
go test ./examples/14-deed-page -v
ZHIZHI_DEED_PAGE_LIVE=1 go test ./examples/14-deed-page -run TestLiveDeedPage -count=1 -v
```

The default tests use local MCP and Chat Completions fixtures to check capability
selection, lazy discovery, tool filtering, the confirmation prompt, a declined
publish that never calls the tool, and the final answer, all through the same `run`
function as the executable. The opt-in live test uses a deterministic local model
fixture against the real endpoint. It publishes a 10-minute public page, checks it
returns 200, deletes it with the returned token and checks for 404.

# blockrun-llm-go-vip

Genuine **native passthrough** for **Anthropic** and **OpenAI** through the BlockRun
gateway — pay per call in USDC (x402) on **Base** or **Solana**
(`vip.WithChain("solana")`), with **zero model substitution and zero response
reshaping**.

Unlike a re-implemented client, the constructors here return the **official
`anthropic-sdk-go` and `openai-go` client types**. They only swap the transport (to add
x402 payment) and the base URL. The gateway returns the upstream provider's response
**verbatim**, so the official SDK parses the real signals:

- **Claude**: real thinking-block `Signature`, native content blocks (text / thinking /
  tool_use), `Usage` cache tokens, native streaming — routed to Anthropic's native
  `/v1/messages`.
- **GPT**: native `ID` (`chatcmpl-*`), `SystemFingerprint`, honest token usage and JSON
  mode — routed to `/v1/chat/completions`.

A Claude / OpenAI relay detector sees a direct upstream call.

This is the Go counterpart of the Python [`blockrun-llm-vip`](../blockrun-llm-vip) package,
reusing the x402 signing and wallet loading from
[`blockrun-llm-go`](../blockrun-llm-go).

## Install

```bash
go get github.com/BlockRunAI/blockrun-llm-go-vip
```

## Use — it's a drop-in

```go
import (
    "github.com/anthropics/anthropic-sdk-go"
    vip "github.com/BlockRunAI/blockrun-llm-go-vip"
)

// Claude — exactly the official anthropic-sdk-go API.
client, _ := vip.NewAnthropic()          // wallet auto-loaded from ~/.blockrun/.session
msg, _ := client.Messages.New(ctx, anthropic.MessageNewParams{
    Model:     anthropic.Model("claude-opus-5"), // current flagship (adaptive thinking)
    MaxTokens: 1024,
    Thinking:  anthropic.ThinkingConfigParamOfEnabled(1024),
    Messages:  []anthropic.MessageParam{
        anthropic.NewUserMessage(anthropic.NewTextBlock("What is 23*47?")),
    },
})
for _, b := range msg.Content {
    if b.Type == "thinking" {
        fmt.Println("signature:", b.Signature)   // real Anthropic signature
    }
}
```

```go
import (
    vip "github.com/BlockRunAI/blockrun-llm-go-vip"
    "github.com/openai/openai-go"
)

// GPT — exactly the official openai-go API.
gpt, _ := vip.NewOpenAI()
resp, _ := gpt.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
    Model:    openai.ChatModel("gpt-4o"),
    Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
})
fmt.Println(resp.SystemFingerprint, resp.Model)   // genuine OpenAI direct
```

Runnable examples: [`examples/anthropic`](examples/anthropic),
[`examples/openai`](examples/openai), and [`examples/seedance`](examples/seedance).

### Models

You name the model; the gateway never substitutes it. Pass any current id verbatim:

- **Claude**: `claude-opus-5` · `claude-sonnet-5` · `claude-opus-4.8` · `claude-opus-4.7` ·
  `claude-opus-4.5` · `claude-sonnet-4.6` · `claude-sonnet-4.5` · `claude-haiku-4.5`.
  Opus 4.7 and up use adaptive thinking — `anthropic.ThinkingConfigParamOfEnabled(N)` is honored.
- **GPT**: `gpt-5.6-sol` · `gpt-5.6-terra` · `gpt-5.6-luna` · `gpt-5.5` · `gpt-5.4` ·
  `gpt-5.3` · `gpt-5.2` · `gpt-4.1` · `gpt-4o` · `gpt-4o-mini`, reasoning `o3` / `o4-mini`,
  and more. GPT‑5.x / o-series are reasoning models — leave `MaxTokens`/`Temperature`
  unset (the gateway normalizes them); `gpt-4o` / `gpt-4o-mini` are served OpenAI-direct.

The SDK also ships an 82-model catalog snapshot (chat, image, video, music, speech, and
sound effects) so callers can build a picker without a network round trip:

```go
for _, model := range vip.Models() {
    fmt.Println(model.ID, model.Categories, model.ContextWindow, model.Pricing)
}
opus, ok := vip.FindModel("anthropic/claude-opus-5")
```

Catalog ids are namespaced (`anthropic/claude-opus-5`), which is the form the media helpers
take. The gateway resolves either form on the chat routes, so `model.ID` works with
`Messages.New` / `Chat.Completions.New` too — but `model.NativeID()` gives the bare
provider id (`"claude-opus-5"`) that matches the upstream SDK docs. `FindModel` accepts
either form. `BillingMode` (`"paid"`, `"free"`, or the media unit) identifies the free
NVIDIA tier; an unpriced media model is not free.

One gotcha: use the dotted ids above. Dashed variants are accepted only as a fixed alias
list, and a namespaced dashed id (`anthropic/claude-sonnet-4-6`) is rejected by
`/v1/messages` outright.

The snapshot covers Claude, all GPT-5.6 tiers, Gemini, DeepSeek, Grok, Qwen, free NVIDIA,
and media models. Use `https://blockrun.ai/api/v1/models` when your application needs the
live catalog.

## Seedance video — incl. real-person (RealFace) & AI character (Portrait)

Generate short videos through **ByteDance Seedance**. The gateway runs generation
asynchronously, and the client mirrors that — `Submit` signs the x402 payment
authorization and returns immediately, then you `Poll` or `Wait`. USDC settles
on-chain only on the first poll that observes `completed`; failed or abandoned
jobs are never charged:

```go
video, _ := vip.NewVideo()

// Async: submit returns a job handle without blocking.
job, _ := video.Submit(ctx, "a neon-lit cyberpunk street, slow dolly forward",
    &vip.VideoGenerateOptions{Model: "bytedance/seedance-2.0-fast", DurationSeconds: 5})
fmt.Println(job.ID, job.Status) // e.g. "bytedance:video_…", "queued"

for !job.Done() {
    time.Sleep(12 * time.Second)
    video.Poll(ctx, job) // re-signs x402, advances job.Status
}
fmt.Println(job.Response.Data[0].URL) // permanent BlockRun-hosted MP4
```

`video.Wait(ctx, job)` blocks until done, and `video.Generate(ctx, prompt, opts)` is
shorthand for `Submit`+`Wait` (one blocking call). `Video` is implemented natively in
this package; `RealFace` and `Portrait` reuse blockrun-llm-go's gateway clients. All
x402-paid on Base.

A specific, real person can appear consistently across clips: **enroll once** via
**RealFace** (one-time $0.01, ~1-min on-phone liveness for consent, no KYC), get a
`ta_xxxx`, and pass it as `RealFaceAssetID` on Seedance 2.0 / 2.0-fast. For an AI
character / mascot use **Portrait** instead (single enroll, no liveness).

```go
portrait, _ := vip.NewPortrait()
asset, _ := portrait.Enroll(ctx, "Mascot", "https://example.com/character.jpg") // $0.01
job, _ := video.Generate(ctx, "the mascot waves in soft studio light",
    &vip.VideoGenerateOptions{Model: "bytedance/seedance-2.0", RealFaceAssetID: asset.AssetID})
```

`RealFaceAssetID` is mutually exclusive with `ImageURL` and only works on Seedance
2.0 / 2.0-fast. Constructors: `vip.NewVideo`, `vip.NewRealFace`, `vip.NewPortrait`.

Full real-person flow (RealFace state machine, on-phone liveness, error states):
**[docs/real-person-flow.md](docs/real-person-flow.md)**.

## Image — generate & edit

```go
img, _ := vip.NewImage()
out, _ := img.Generate(ctx, "a red fox in fresh snow, soft studio light",
    &vip.ImageGenerateOptions{Model: "openai/gpt-image-1"})
fmt.Println(out.Data[0].URL)

// edit / multi-image fusion:
edited, _ := img.Edit(ctx, "make it night", []string{dataURI}, nil)
```

Models: `openai/gpt-image-1` · `gpt-image-2` · `google/nano-banana` · `nano-banana-pro` ·
`xai/grok-imagine-image` · `zai/cogview-4`. `img.ListImageModels(ctx)` lists them (free).

## Audio — speech, music, sound effects

```go
sp, _ := vip.NewSpeech()
speech, _ := sp.Generate(ctx, "Hello there.", &vip.SpeechGenerateOptions{Voice: "sarah"})
sfx, _ := sp.SoundEffect(ctx, "distant thunder over rain", nil)

mu, _ := vip.NewMusic()
instrumental := true
track, _ := mu.Generate(ctx, "dreamy lo-fi beat", &vip.MusicGenerateOptions{Instrumental: &instrumental})
fmt.Println(speech.Data[0].URL, sfx.Data[0].URL, track.Data[0].URL)
```

`sp.ListVoices(ctx)` lists TTS voices (free). Music runs ~1-3 min and blocks until ready.

## Search — Grok Live Search & Exa

```go
s, _ := vip.NewSearch()
r, _ := s.Search(ctx, "latest on x402 micropayments",
    &vip.SearchOptions{Sources: []string{"x", "news"}, MaxResults: 15})

exa, _ := vip.NewExa()
hits, _ := exa.Search(ctx, "x402 protocol", map[string]any{"numResults": 5})
text, _ := exa.Contents(ctx, []string{"https://example.com"}, nil)
ans, _ := exa.Answer(ctx, "what is the x402 payment header?", nil)
```

## Voice & Phone — AI phone calls

Lease a wallet-bound number, then place an AI-driven outbound call. `Call` returns a
call id; poll it with `GetCallStatus` for the transcript + recording.

```go
phone, _ := vip.NewPhone()
num, _ := phone.BuyNumber(ctx, vip.BuyNumberOptions{Country: "US", AreaCode: "415"}) // $5, 30-day lease

voice, _ := vip.NewVoice()
call, _ := voice.Call(ctx, vip.CallOptions{
    To:          "+14155551234",
    Task:        "Ask if they're open Sunday, confirm hours, then thank them and end the call.",
    MaxDuration: 3,
})
status, _ := voice.GetCallStatus(ctx, call.CallID)
fmt.Println(status)
```

`Phone` also does `Lookup` / `LookupFraud`, `ListNumbers`, `RenewNumber`, `ReleaseNumber`.

## Options

```go
vip.NewAnthropic(
    vip.WithChain("solana"),                     // "base" (default) or "solana"
    vip.WithWalletKey("..."),                    // explicit key (Base hex, or bs58 on Solana)
    vip.WithBaseURL("https://blockrun.ai/api"),  // override the gateway
    vip.WithSolanaRPCURL("https://..."),         // override the Solana RPC (Solana only)
    vip.WithAPIKey("blockrun"),                  // placeholder upstream key
    vip.WithFacilitator("payai"),                // facilitator preference (Solana only; default "figment")
)
```

All options apply to every constructor (`NewOpenAI`, `NewImage`, `NewVideo`, …).

## Solana

Pass `vip.WithChain("solana")` to any constructor to pay **USDC on Solana** via
`sol.blockrun.ai` instead of Base — the drop-in stays identical:

```go
claude, _ := vip.NewAnthropic(vip.WithChain("solana"))  // bs58 key from ~/.blockrun/.solana-session
gpt, _    := vip.NewOpenAI(vip.WithChain("solana"))
img, _    := vip.NewImage(vip.WithChain("solana"))
```

Payment is the x402 **SVM "exact" scheme**: the bs58 key signs a Solana
`TransferChecked` USDC transaction locally (ed25519), and BlockRun's facilitator
co-signs the fee and settles it gaslessly. Responses are still the upstream
provider's verbatim JSON. Runnable example: [`examples/solana`](examples/solana).

### Facilitator routing (v0.7.0+)

Solana VIP clients send a facilitator preference (`x-blockrun-facilitator:
figment`) plus the wallet's **public** address (`x-payer-wallet`) with each
request, so VIP traffic settles through the **Figment** facilitator while
other BlockRun clients keep using PayAI. Payment semantics are identical on
both rails (same x402 exact-scheme USDC transfer, gasless for the payer). Opt
out with `vip.WithFacilitator("payai")` or `BLOCKRUN_FACILITATOR=payai` — the
wire is then byte-identical to v0.6.x. Base chain is unaffected.

## Wallet

The private key is used **only for local signing** (EIP-712 on Base, SVM/ed25519 on
Solana) and never leaves your machine.

**Base** resolution order:

1. `WithWalletKey(...)` option
2. `BLOCKRUN_WALLET_KEY` env
3. `BASE_CHAIN_WALLET_KEY` env
4. `~/.blockrun/.session`

**Solana** (`WithChain("solana")`) resolution order:

1. `WithWalletKey(...)` option (bs58)
2. `SOLANA_WALLET_KEY` env
3. `~/.*/solana-wallet.json` (most recent)
4. `~/.blockrun/.solana-session`

The gateway base URL can be overridden via `BLOCKRUN_API_URL` (Base) /
`BLOCKRUN_SOLANA_API_URL` (Solana); the Solana signing RPC via `SOLANA_RPC_URL`.

## How it works

`NewAnthropic` / `NewOpenAI` build the official SDK client with two request options:

- `option.WithBaseURL(...)` — point at the BlockRun gateway.
- `option.WithMiddleware(...)` — a transport middleware that performs the x402
  handshake: the first request goes out unpaid; on a `402` the `payment-required`
  requirement is parsed, a USDC authorization is signed locally (EIP-712 on Base, the
  SVM "exact" scheme on Solana), and the original request is replayed **verbatim** with
  a `PAYMENT-SIGNATURE` header. On `200` the upstream body is handed back untouched.

Because the middleware never reshapes the success response, the official SDK does all
parsing — that is what makes the passthrough native.

Two failures recover automatically, both bounded, and both only on an explicit
machine-readable signal from the gateway:

- **Stale blockhash** (`402`) — the signed Solana transaction aged out. It is discarded,
  never replayed; the whole negotiation re-runs from a fresh quote and a fresh signature.
- **Verifier unavailable** (`503 PAYMENT_VERIFICATION_UNAVAILABLE`) — the gateway could
  not run its payer-risk screen and failed closed, so the payment was never judged.
  The request is retried honouring the server's `Retry-After` (capped at 30s).

Every other response, including any other `503`, is handed to the caller untouched. That
is deliberate: paid routes settle optimistically, in parallel with the upstream work, so
an unmarked `503` may arrive *after* the charge — retrying it would buy the same thing
twice. Recovery is limited to answers the gateway gives before anything is broadcast.

## Scope

Covers, on **Base** or **Solana** (`vip.WithChain("solana")`):

- **Anthropic + OpenAI** native passthrough (`NewAnthropic`, `NewOpenAI`).
- **Seedance video** incl. RealFace real-person + Virtual Portrait (`NewVideo`,
  `NewRealFace`, `NewPortrait`).
- **Image** generate + edit (`NewImage`).
- **Audio**: ElevenLabs speech + sound effects (`NewSpeech`), MiniMax music (`NewMusic`).
- **Search**: Grok Live Search (`NewSearch`) + Exa web search (`NewExa`).
- **Voice & Phone**: AI phone calls (`NewVoice`) + number provisioning (`NewPhone`).

## Access

Give BlockRun your wallet address to enable VIP, then pay per call from that wallet.

Contact: vicky@blockrun.ai

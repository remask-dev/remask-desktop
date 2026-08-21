# Remask Desktop

**A local-first PII protection gateway for AI apps and APIs.**

[简体中文](README.zh-CN.md)

Remask sits between your AI clients and AI providers. It detects sensitive information, replaces it with safe placeholders before a request leaves your device, and can restore the protected values in supported responses.

Use Remask with AI coding assistants, desktop apps, browsers, SDKs, scripts, and internal tools—without sending raw personal or confidential data directly to an AI service.

## Why Remask

AI workflows often include names, email addresses, account details, credentials, customer records, source code, and other sensitive context. Manually removing that information is slow and easy to forget.

Remask provides a reusable privacy layer for all of these workflows:

- Sensitive data is detected and masked locally before transmission.
- Existing AI tools can connect through a familiar API base URL or a local proxy.
- Protection policies stay consistent across apps, providers, and team workflows.
- Request activity can be reviewed without exposing the original sensitive values by default.

## Core features

### Local PII detection and masking

Remask combines local detection models with deterministic rules to identify information such as:

- Names, phone numbers, email addresses, and physical locations
- Account numbers, bank card data, and government-issued identifiers
- Usernames, passwords, API keys, secrets, and PINs
- IP addresses, device identifiers, URLs, and other custom patterns

Detected values are replaced with typed placeholders before protected requests are sent to the AI provider.

### Two ways to protect AI traffic

**API Gateway** — Use a Remask URL as the base URL in an SDK, script, service, or other API client. Remask applies the selected provider and protection policy before forwarding the request.

**Proxy Gateway** — Route an existing application through the local HTTP/HTTPS or SOCKS5 proxy. Add protected target domains and Remask will inspect and mask matching AI traffic while preserving the application's original destination.

### Protected app launch

Start supported tools directly from Remask with the required proxy and certificate environment already configured. Quick launch supports common AI development workflows such as Codex, Codex CLI, Claude Code, OpenCode, a browser, a terminal, and other installed applications.

This keeps protection scoped to the launched application and does not require changing global shell or system proxy settings.

### Flexible protection policies

- Enable or disable individual PII categories.
- Add regular-expression rules for organization-specific identifiers.
- Choose whether system messages and previous AI responses are included in masking.
- Pause all protection when an explicit unredacted request is required.
- Optionally restore protected values in supported AI responses.

### Local visibility and control

The desktop dashboard shows request volume, protected entities, latency, token usage, and recent activity. A built-in masking test lets you verify a policy locally before connecting an application.

Request logs make it possible to confirm which fields were protected and what was actually sent to the AI service. Log content, retention, and cleanup are configurable.

### Provider and model management

Configure multiple AI providers, keep managed credentials on the device, switch local detection models, and choose the available inference device. Remask also supports custom request adapters for provider-specific formats.

### Desktop experience

- System tray controls and optional launch at login
- Guided local CA certificate installation for protected HTTPS traffic
- English, Simplified Chinese, Japanese, and German interfaces
- macOS and Windows release packages, with Linux source builds supported

## How it works

```text
AI app or SDK
      |
      v
Remask detects and masks PII locally
      |
      v
AI provider receives protected content
      |
      v
Remask can restore placeholders in the response
```

The AI provider still receives the non-sensitive parts of the request and the generated placeholders. The original detected values remain on the device during normal protected operation.

## Common use cases

- Use AI coding tools without exposing credentials, internal hosts, or customer data copied into prompts.
- Summarize support tickets, documents, or CRM exports after personal details are masked.
- Add a privacy layer in front of OpenAI-compatible SDKs and internal AI applications.
- Apply the same PII policy across several AI providers.
- Inspect protected request history for privacy reviews and troubleshooting.

## Get started

1. Download the latest installer from [GitHub Releases](https://github.com/remask-dev/remask-desktop/releases/latest).
2. Open Remask and confirm the built-in masking test works with your sample text.
3. Choose an access method:
   - Add an AI provider and copy its API Gateway base URL.
   - Add a protected target and connect through the Proxy Gateway.
   - Use Quick Launch to start an application in a protected environment.
4. Send a test request and verify the result in **Request Logs**.

## Privacy boundaries

Remask protects traffic only when global protection is enabled and the request matches a configured provider or protected target. Unmatched proxy traffic is forwarded transparently without inspection or masking.

For HTTPS proxy protection, Remask uses a local CA certificate. Its private key remains on the device. Managed provider credentials are kept locally and excluded from request logs. Debug or raw-content logging may contain sensitive information and should only be enabled when necessary.

Remask reduces accidental PII disclosure, but it should be used as one layer of a broader security and data-governance program.

## For contributors

The main README focuses on the product. For a basic frontend check:

```bash
npm install
npm run typecheck
npm run build
```

Desktop development and packaging commands are available in [`package.json`](package.json) and the [`scripts`](scripts) directory.

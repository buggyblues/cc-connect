# ShadowOB Platform Guide

`shadowob` connects cc-connect to Shadow through the Shadow REST API and Socket.IO gateway. It supports channel and DM messages, media download/upload, typing indicators, compact streaming previews, native Shadow slash command registration, and Shadow interactive blocks for buttons/forms.

## Setup

Use only a Shadow Buddy token in cc-connect. Server and channel membership stay managed in Shadow.

1. Open Shadow and go to Settings -> Buddy Management.
2. Create or open the Buddy that should represent cc-connect.
3. Add or bind that Buddy to the target server/channel in Shadow.
4. Enable the Buddy policy for the channel (`listen=true`, `reply=true`; enable mention-only only when desired).
5. Generate the Buddy token and set it as `SHADOWOB_AGENT_TOKEN`.

Minimal config:

```toml
[[projects.platforms]]
type = "shadowob"

[projects.platforms.options]
token = "${SHADOWOB_AGENT_TOKEN}"
allow_from = "*"
```

`server_url` defaults to `https://shadowob.com`. For a private or self-hosted Shadow instance, override it:

```toml
server_url = "https://shadow.example.com"
```

Server and channel routing is read from the Buddy policy via `/api/agents/:id/config`. cc-connect also tracks Shadow policy change events and joins newly enabled channels without requiring channel IDs in `config.toml`.

## Options

| Option | Default | Notes |
| --- | --- | --- |
| `token` | required | Shadow Buddy token. |
| `server_url` | `https://shadowob.com` | Shadow API and Socket.IO base URL. A trailing `/api` is stripped. |
| `allow_from` | empty | Shadow user IDs or usernames. Empty or `*` allows all senders and logs a warning. |
| `listen_dms` | `true` | Join accessible DM rooms. |
| `share_session_in_channel` | `false` | Share one agent session per channel/thread instead of per user. |
| `progress_style` | `compact` | Enables message edit previews. Use `legacy` to prefer separate messages. |
| `media_max_bytes` | `20971520` | Max inbound media download size. |
| `slash_commands_path` | `$SHADOW_SLASH_COMMANDS_PATH` | Optional Shadow/OpenClaw-style slash command JSON. |

## Media

Inbound Shadow attachments are downloaded with the configured token and capped by `media_max_bytes`. Images are passed to the agent as images, audio as voice/audio, and other content as files.

Outbound media uses the standard cc-connect side channel:

```bash
cc-connect send --image /path/to/chart.png
cc-connect send --file /path/to/report.pdf
```

The platform creates a placeholder Shadow message and attaches uploaded media to it through `/api/media/upload`. Media bytes are not embedded into message JSON: cc-connect does not generate `data:` URLs, blob URLs, or base64 attachment payloads for ShadowOB.

## Forms And Slash Commands

cc-connect registers its built-in slash commands to Shadow when the Buddy token resolves to an agent. You can also register Shadow/OpenClaw-style commands with forms:

```json
[
  {
    "name": "deploy",
    "description": "Deploy with a selected environment",
    "body": "Validate the selected environment, summarize the plan, then run the deployment if appropriate.",
    "interaction": {
      "kind": "form",
      "prompt": "Choose deployment options",
      "submitLabel": "Deploy",
      "fields": [
        {
          "id": "environment",
          "kind": "select",
          "label": "Environment",
          "required": true,
          "options": [
            { "id": "staging", "label": "Staging", "value": "staging" },
            { "id": "prod", "label": "Production", "value": "production" }
          ]
        },
        {
          "id": "notes",
          "kind": "textarea",
          "label": "Notes"
        }
      ]
    }
  }
]
```

Configure it:

```toml
slash_commands_path = "${SHADOW_SLASH_COMMANDS_PATH}"
```

When a user invokes `/deploy` without arguments, cc-connect sends a Shadow form. The submitted form comes back through Shadow as an interactive response and is forwarded to the agent as structured text. The local command `body` is used only inside cc-connect; it is not registered back to Shadow.

## Build Tags

`shadowob` is compiled by default. To exclude it:

```bash
go build -tags 'no_shadowob' ./cmd/cc-connect
```

To include only ShadowOB and selected agents:

```bash
make build AGENTS=claudecode,opencode PLATFORMS_INCLUDE=shadowob
```

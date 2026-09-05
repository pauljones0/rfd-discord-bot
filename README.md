# RFD Discord Bot

RedFlagDeals alerts for your Discord server, running as one small Go service with
its own SQLite database. It watches RFD Hot Deals, groups duplicate threads,
follows engagement, and updates existing Discord messages as deals change.

Choose all deals, tech deals, warm/hot deals, or hot deals. Optional Gemini title
cleanup is available; ordinary alerts work without an AI account.

## Run with Docker

You need Docker with Compose and a Discord application of your own.

Clone the public project:

```sh
git clone https://github.com/pauljones0/rfd-discord-bot.git
cd rfd-discord-bot
```

1. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications).
   Copy its **Application ID** and **bot token**. Leave **Interactions Endpoint
   URL empty**; this bot receives commands through an outbound Gateway connection.
   No public domain, port forwarding, reverse proxy, or VPN is needed.
2. Under Installation, enable **Guild Install** with the `bot` and
   `applications.commands` scopes. Give the bot **View Channels**, **Send
   Messages**, and **Embed Links** permissions. Use the install link to add it to
   your server. Privileged intents can remain disabled. Discord's
   [app setup guide](https://docs.discord.com/developers/quick-start/getting-started)
   explains the portal settings; this project uses Gateway rather than the HTTP
   transport in that tutorial.
3. In this repository, copy `.env.example` to `.env` and fill in
   `DISCORD_APP_ID` and `DISCORD_BOT_TOKEN`. Optionally set `DISCORD_GUILD_ID` to
   register commands immediately in one server. Leave it blank for global commands.
4. Build, register the command, and start:

   ```sh
   docker compose build
   docker compose run --rm bot check-config
   docker compose run --rm bot register
   docker compose up -d
   ```

5. In Discord, use `/rfd subscribe`, select a channel, and choose a filter.
   Allow the bot to send messages and embed links in that channel. The next poll
   normally starts within three minutes. Only users with **Manage Server** or
   administrator permission can manage subscriptions.

Use a dedicated application for this standalone deployment. Running it with
another bot process's token can cause competing Gateway sessions and command
ownership problems. Registration upserts only `/rfd`; it does not bulk-delete
other application commands. See [Discord's command documentation](https://docs.discord.com/developers/interactions/application-commands).

## Commands and filters

| Command | What it does |
| --- | --- |
| `/rfd subscribe channel filter` | Adds or updates a channel/filter subscription |
| `/rfd unsubscribe channel [filter]` | Removes one filter, or all RFD filters in that channel |
| `/rfd list` | Lists this server's subscriptions |

Management replies are private to the person issuing the command. A channel can
have several filters; an eligible deal is still posted only once in that channel.

| Filter | Included deals |
| --- | --- |
| All deals | Every accepted RFD deal |
| Tech only | Accepted deals classified as tech |
| Warm + Hot (all) | Discount-backed deals reaching the warm or hot threshold |
| Warm + Hot (tech) | The same, limited to tech |
| Hot only (all) | Discount-backed deals reaching the hot threshold |
| Hot only (tech) | The same, limited to tech |

Warm/hot status uses the existing RFD engagement rules, with a fallback when RFD
does not expose view counts. It is an alert filter, not an assurance that an item
is worth buying. Existing messages retain their IDs in SQLite so ordinary
restarts do not repost the same deal. With no subscriptions, the bot skips
scraping and title-cleaning work.

## Configuration

The supplied Compose file loads `.env`. A native binary reads environment
variables directly; it does not search other projects for configuration.

| Variable | Default / purpose |
| --- | --- |
| `DISCORD_APP_ID` | Required: your application ID |
| `DISCORD_BOT_TOKEN` | Required: your bot token |
| `DISCORD_GUILD_ID` | Optional registration scope; blank means global |
| `RFD_POLL_INTERVAL` | `3m`, delay between completed polls |
| `RFD_POLL_TIMEOUT` | `4m`, time limit for one poll |
| `DISCORD_UPDATE_INTERVAL` | `10m`, minimum spacing between edits |
| `MAX_STORED_DEALS` | `2000`, retained deal limit |
| `LOG_LEVEL` | `INFO` |
| `SQLITE_PATH` | `data/rfd.sqlite` natively; `/data/rfd.sqlite` in Compose |
| `LISTEN_ADDR` | `127.0.0.1:8080`, local health checks |
| `GEMINI_API_KEY` | Optional title cleanup; blank disables it |
| `GEMINI_MODELS` | Required only when enabling Gemini; comma-separated supported model IDs in fallback order |
| `AMAZON_AFFILIATE_TAG` | Optional; blank removes Amazon affiliate tags |
| `BESTBUY_AFFILIATE_PREFIX` | Optional HTTPS redirect prefix; blank keeps direct links |

There are no embedded personal affiliate IDs. eBay product links found inside
RFD threads are normalized to direct item URLs. This does not run an eBay monitor.

The scraper uses Go HTTP requests and HTML parsing. A browser, Python, Node,
local LLM server, cloud database, and the original combined bot are not needed.

## Operations

```sh
docker compose logs -f bot
docker compose ps
docker compose exec bot /rfd-bot healthcheck
docker compose run --rm bot check-storage
docker compose down
```

The named `rfd-data` volume retains subscriptions and deal/message history across
container replacements. `check-storage` opens or initializes that volume and
checks SQLite without contacting Discord. Back up the volume while the bot is
stopped, or use SQLite's backup API; do not copy only the main database file while
WAL writes are active. Removing the data volume also removes notification history.

No ports are published. The health server is on loopback inside the container.
`/health` reports database status and the last poll timestamps/failure flag;
`/health/discord` reports Gateway readiness and response counters. Gateway
disconnects are retried automatically. A scrape failure is logged and retried on
the next scheduled poll; it is not treated as proof that no deals exist.

If commands do not arrive, check that the application's Interactions Endpoint
URL is empty and the bot token belongs to the configured application ID. Startup
checks both. If commands are missing, rerun `register`; global registration may
take time to appear. If alerts cannot be sent, inspect the bot's channel permissions
and logs. Remote HTML changes, rate limiting, or an unavailable optional Gemini
model may require updated selectors or settings.

## Develop and verify

Requires Go 1.26 or later; the Docker build and CI use Go 1.27.

```sh
go test -race -tags=integration ./...
go vet ./...
go build -o rfd-bot ./cmd/rfd
```

Tests run against local HTTP/WebSocket fixtures and temporary SQLite databases.
They do not need real credentials, post to real channels, or scrape live sites.
The full local pipeline test exercises scraping, SQLite persistence, Discord
notification formatting/delivery, overlapping filters, and restart deduplication.
Gateway tests exercise command receipt, acknowledgement, disconnection, and resume.

| Package | Responsibility |
| --- | --- |
| `cmd/rfd` | Startup, scheduling, shutdown, registration, health checks |
| `internal/api` | RFD commands and outbound Discord Gateway transport |
| `internal/scraper` | RFD HTML and detail parsing |
| `internal/processor` | Deduplication, enrichment, filtering, message updates |
| `internal/notifier` | RFD Discord embeds and HTTP delivery |
| `internal/storage` | This bot's SQLite tables and subscriptions |
| `internal/ai` | Optional Gemini title cleanup |

The domain interfaces stay small: processor storage, scraper, notifier, validator,
and optional title analyzer. Do not import another bot to reuse one formatter or
storage method. See [the extraction record](ORIGIN.md) and [validation](VALIDATION.md).

## Sharing

The canonical repository is
[pauljones0/rfd-discord-bot](https://github.com/pauljones0/rfd-discord-bot).
It contains only the standalone RFD service. Older clones of the combined bot
belong to the separately maintained `homelab-bots` project; do not pull this
repository into an existing combined deployment. See [migration differences](ORIGIN.md).

Share this repository or its source archive. Recipients supply their own Discord
application and `.env`; no credentials or existing subscriptions are included.
The Git and Docker ignore rules exclude local environment files, databases, and
logs. The existing [license](LICENSE) and source attribution are preserved.

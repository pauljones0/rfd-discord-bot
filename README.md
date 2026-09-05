# RFD Discord Bot

RedFlagDeals alerts for your Discord server, running as one small Go service with
its own SQLite database. It watches RFD Hot Deals, groups duplicate threads,
follows engagement, and updates existing Discord messages as deals change.

Choose all deals, tech deals, warm/hot deals, or hot deals. Optional Gemini title
cleanup is available; ordinary alerts work without an AI account.

**[Add the hosted bot to your server](https://discord.com/oauth2/authorize?client_id=1545646915943927828&scope=bot%20applications.commands&permissions=19456&integration_type=0)**
· [Host your own copy](#host-your-own-copy-with-docker)
· [Commands and filters](#commands-and-filters)

## Get alerts in your server

The maintainer-hosted **rfdSOLO** bot can serve multiple Discord servers. You only
need Discord and **Manage Server** or administrator permission in your server;
the maintainer runs the service and supplies its credentials.

1. Use **Add the hosted bot to your server** above and select your server. The
   invite requests **View Channels**, **Send Messages**, and **Embed Links**.
2. Create or choose a text channel such as `#rfd-deals`. Make sure the bot has
   those three permissions in that channel, including any private-channel overrides.
3. Run `/rfd subscribe`, select that channel, and choose a filter from the menu,
   such as **Warm + Hot (tech)**. Use **All deals** for the broadest feed.
4. Run `/rfd list` to confirm the saved subscription. Matching deals from the
   currently observed feed arrive on a subsequent poll, normally within a few
   minutes. A filter with no qualifying deals will stay quiet. The confirmation
   means the subscription was saved; it does not verify channel permissions.

Subscriptions deliver **server channel posts**, not personal DMs. Everyone who
can view the channel can read its alerts. For personal notifications, set that
channel's Discord notification preference to **All Messages** (or **All**);
alerts do not ping `@everyone` or individual members. Discord's
[notification guide](https://support.discord.com/hc/en-us/articles/215253258-Notifications-Settings-101)
explains channel overrides and device settings. Members without Manage Server
can ask a server admin to set up the channel.

Use `/rfd unsubscribe` to stop a channel/filter subscription. Several channels
or servers can subscribe independently. The hosted option depends on the
maintainer's running service; you can also [host your own copy](#host-your-own-copy-with-docker).

If installation only adds commands and no bot member, use the invite above: it
explicitly requests both the `bot` and `applications.commands` scopes and a server
installation. A commands-only or personal-account installation cannot deliver
these scheduled channel alerts. See Discord's
[bot installation flow](https://docs.discord.com/developers/topics/oauth2#bot-authorization-flow).

## Host your own copy with Docker

You need Docker with Compose and a Discord application of your own.
These steps run an independent bot under your control. The
[architecture record](ARCHITECTURE.md) explains the Go/SQLite design, and the
[release review](REVIEW.md) records the audit findings and validation.

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
   your server. If others will install your instance, enable **Public Bot** on
   the Bot page and register commands globally (leave `DISCORD_GUILD_ID` blank).
   This bot uses server installation; personal **User Install** is not needed.
   Privileged intents can remain disabled. Discord's
   [app setup guide](https://docs.discord.com/developers/quick-start/getting-started)
   explains the portal settings; this project uses Gateway rather than the HTTP
   transport in that tutorial.
3. Run `python3 scripts/configure_discord.py` to paste your **Application ID**,
   optional **Public Key**, and **bot token** at the prompts. The helper accepts
   raw values or labels such as `Application ID: 123456789012345678`. Token input
   is hidden. It writes an owner-only `.env`, preserves other settings, and can
   be rerun to update credentials. A public key is saved for reference; Gateway
   commands need the bot token. You can press Enter to add the token later.
   Use `--env-file /path/to/.env` for a different config location.
   Alternatively, copy `.env.example` to `.env` and fill in `DISCORD_APP_ID` and
   `DISCORD_BOT_TOKEN` manually. Optionally set `DISCORD_GUILD_ID` to register
   commands immediately in one server. Leave it blank for global commands.
4. Build, register the command, and start:

   ```sh
   docker compose build
   docker compose run --rm bot check-config
   docker compose run --rm bot check-discord
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
| `SELECTORS_CONFIG_PATH` | Optional JSON selector override; blank uses the embedded configuration |
| `SQLITE_PATH` | `data/rfd.sqlite` natively; `/data/rfd.sqlite` in Compose |
| `LISTEN_ADDR` | `127.0.0.1:8080`, local health checks |
| `GEMINI_API_KEY` | Optional title cleanup; blank disables it |
| `GEMINI_MODELS` | Required only when enabling Gemini; comma-separated supported model IDs in fallback order |
| `AMAZON_AFFILIATE_TAG` | Optional; blank removes Amazon affiliate tags |
| `BESTBUY_AFFILIATE_PREFIX` | Optional HTTPS redirect prefix; blank keeps direct links |

Gemini is used only to shorten deal titles. Categories, popularity thresholds,
and discount checks determine which deals qualify for a subscription. To enable
cleanup with a lightweight model, set your own key and an explicit model ID:

```dotenv
GEMINI_API_KEY=your-own-key
GEMINI_MODELS=gemini-3.5-flash-lite
```

This example was verified in September 2026. Check Google's
[model catalog](https://ai.google.dev/gemini-api/docs/models) for availability in
your account. Model names are configured explicitly; installing a newer SDK does
not automatically switch them. Titles are cleaned in small batches during the
same poll and saved even when the source listing is unchanged. Cleanup has a
30-second budget per poll; failed or slow cleanup leaves original titles usable
and missing cleanup can retry later. An exhausted
model/key configuration pauses cleanup for its saved cooldown instead of sending
requests on every poll. Existing Discord message ownership is preserved.

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
checks both and binds its SQLite database to that application ID. An existing
binding must match; a nonempty database without a binding is rejected instead of
silently adopting another bot's history. `check-discord` checks the application
and the bot's permissions in subscribed channels without posting messages. With
no subscriptions yet, it cannot check a destination channel.

If commands are missing, rerun `register`; global registration may
take time to appear. If alerts cannot be sent, inspect the bot's channel permissions
and logs. Remote HTML changes, rate limiting, or an unavailable optional Gemini
model may require updated selectors or settings.

## Migrate existing RFD alerts

New installations can start directly with `/rfd subscribe`. Existing deployments
can import subscriptions and deal/message history using the offline `import`
command. The original combined database is a different schema and must not be
mounted as this bot's database.

Follow [the migration procedure](MIGRATION.md) to rehearse the import into a new
database, preserve notification receipts, and stop the old RFD producer before
activating the new one. Imported messages remain in Discord and retain their
deduplication receipts; messages authored by the old application are not edited
by the new application. Set `MAX_STORED_DEALS` high enough to retain the imported
history, including room for new deals.

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
| `cmd/rfd` | Startup, scheduling, registration, preflight, offline import, health checks |
| `internal/api` | RFD commands and outbound Discord Gateway transport |
| `internal/scraper` | RFD HTML and detail parsing |
| `internal/processor` | Deduplication, enrichment, filtering, message updates |
| `internal/notifier` | RFD Discord embeds and HTTP delivery |
| `internal/storage` | This bot's SQLite tables and subscriptions |
| `internal/ai` | Optional Gemini title cleanup |

The domain interfaces stay small: processor storage, scraper, notifier, validator,
and optional title analyzer. Deal reconciliation and title batches belong to one
poll; transport does not own eligibility rules. Successful channel receipts are
saved per deal even when another channel fails, and missing destinations retry
on unchanged observed deals. Do not import another bot to reuse one formatter or
storage method. See [the extraction record](ORIGIN.md) and [validation](VALIDATION.md).

## Sharing

The canonical repository is
[pauljones0/rfd-discord-bot](https://github.com/pauljones0/rfd-discord-bot).
It contains only the standalone RFD service. Older clones of the combined bot
belong to the separately maintained `homelab-bots` project; do not pull this
repository into an existing combined deployment. See [migration differences](ORIGIN.md)
and [the cutover procedure](MIGRATION.md).

For people who want alerts, share the **Add the hosted bot to your server** link
at the top and the [subscription steps](#get-alerts-in-your-server). They use the
running service; each server manages its own channel subscriptions.

For people who want to run an independent instance, share this repository or its
source archive. They supply their own Discord application and `.env`; no
credentials or existing subscriptions are included. The Git and Docker ignore
rules exclude local environment files, databases, and logs. The existing
[license](LICENSE) and source attribution are preserved.

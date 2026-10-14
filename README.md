# TVT

A free, self-hostable TV show tracker: follow shows, mark episodes watched, see what is next, get notified when new episodes air, and talk about each episode with spoiler protection. One Go binary (templates, assets and 10 languages embedded), SQLite storage, installable PWA.

See it in action at [tvt.carl-sinclair.com](https://tvt.carl-sinclair.com).

Licensed under the [GNU AGPL v3](LICENSE); see [NOTICE](NOTICE) for attribution, [PRIVACY.md](PRIVACY.md) for what the software stores, and [SECURITY.md](SECURITY.md) to report a problem.

## Features

- Exact per-episode air times from TVmaze, then Trakt if configured, where a real broadcast time is known (placeholders are ignored); otherwise the air date at midday in the user's own time zone. Users never have to enter an air time; they only choose how long before or after it to be alerted.
- Second catalog from TheTVDB (optional, key in Admin > Config) for shows TMDB lacks, such as YouTube and web series. TheTVDB requires attribution, which TVT shows on the About page.
- Show catalogue from TMDB: synopses, ratings, posters and per-episode stills, kept fresh by a changes-feed poller; titles and synopses are always stored in each user's own language: every followed show is fetched in the language of each user who follows it, in the background, and refreshed weekly.
- Import from TV Time: upload the GDPR export zip in Settings. Shows are matched through TMDB's TVDB lookup, watched episodes keep their original dates, followed, favorite and saved-for-later shows come across, and nothing you already track is changed. Movies are not imported. The zip is read in memory and not kept.
- Friends: add people by exact username (no fuzzy search), a private friends list, and a home feed of what friends watch, rate and comment on. Spoiler comments never appear, and an episode is only named to a friend who has seen it. Blocked people see nothing. Friendships are addressed by handle so they can later span servers. People can post updates on their own profile; replies are ordinary comments, with the same spoiler flags, reports and moderation.
- Federation: add friends on other TVT servers by handle (`name@server`). Servers know each other by a signing key learned from `/.well-known/tvt` on first contact and pinned afterwards; every message is signed. Friend requests go to the friend's server. Activity (kept 14 days) and episode comments (leased for a day and re-confirmed by their home server, so deletions reach every copy within a day) are shared with every server the admin allow-lists; friendship decides what shows in a feed. Replies work across servers: you can answer a comment from another server and its author is notified there. Comments tagged as spoilers travel flagged as spoilers. Turn federation off in Admin > Servers, block servers per server there, and allow-list per server with its Share switch.
- Public homepage for visitors, a standard Contact page (email and Telegram buttons, then a captcha-guarded form that sends to the operator on Telegram or email; the Telegram bot's /support command opens a chat that is relayed to the operator, or to a group chat, who answers by replying; an operator-written FAQ in Admin > FAQ is offered first and every bot reply carries a Talk to a human button), a built-in privacy policy that can be customized, and optional operator-written pages (terms and so on) in every language, edited in Admin > Pages and linked from the footer.
- Admin dashboard: overview (counts, system status, recent emails and whether they sent, recent errors), config (mail, storage, API keys, Telegram, site identity, with test buttons), user management (search, add, edit, ban, delete, open or close sign-ups), servers (add, block, forget) and the report queue, with badges for what is waiting.
- Usernames can be changed (once every 30 days); the old name redirects and stays reserved for 14 days, and friends on other servers are told. People can deactivate their own account, and whenever an account is banned, deactivated or deleted, its comments can be hidden, deleted or kept, with replies never lost.
- Tracking: follow, status (new follows start as Watching, and My shows opens on Watching), favorites, mark episode / season / everything-up-to-here, ratings, up-next, calendar. Up next is kept in its own table and updated incrementally, so the home page stays fast.
- Show and season pages use the cover art as a backdrop, with rating charts per season and per episode.
- Episode comments with a spoiler model built around one-click reports, plus abuse reports, blocking and an admin queue. Comment text supports a small Markdown subset, with a cheat sheet in the composer.
- Comments and profile posts can carry one picture or GIF: upload a file (JPEG, PNG, WebP up to 16 MB, scaled down to 1920 px; animated GIFs up to 12 MB and 1280 px, rebuilt from their header, colour tables, timing and frames so comments, extra blocks and trailing bytes cannot carry hidden data) or pick a still from the episode itself, copied at full size. Pictures go to the same bucket as the other media, are shown under the same spoiler cover, and are removed with the comment; uploads nobody used are deleted when the draft drops them or after a day. Other servers receive a picture as a link to where it lives. A server publishes its picture host in its well-known document, and approving a server (Admin > Servers) adds that host to the content policy's img-src, so your visitors' browsers load its comment pictures straight from it; deleting the comment or the picture removes it everywhere at once, and an unapproved server's pictures are never loaded. The composer has one photo button: it offers an upload or (on episode pages, when the episode has any) a moment from the episode, and the chosen picture opens in the editor. The editor has three icon-only tools and an undo button: crop and zoom (shape, zoom, rotate, flip), collage (layouts, more pictures, background, spacing) and text (any number of lines with size, colour, outline and capitals, dragged into place). The result is saved as one ordinary picture; animated GIFs are attached directly because a canvas cannot keep them moving.
- Marking an episode as watched while earlier aired episodes are not asks whether to mark everything up to it.
- Accounts: first factor by password, passkey, magic link or Telegram; optional TOTP two-step verification (with recovery codes) as a second factor on top of any of them.
- Notifications: in-app (live over SSE), Web Push, email and Telegram, with a per-category preference matrix, test buttons, and an alert-timing offset (before or after release, up to 7 days either way).
- Public profile with avatar and banner, favorites, favorite genres and watch-time stats (lifetime, today, 7 days, 30 days).
- Languages: English, Portuguese, Spanish, Italian, French, Chinese, Russian, Japanese, Korean, Arabic (right-to-left).
- Data export and account deletion, a built-in privacy policy page in every language, and no analytics or third-party scripts.
- Optional S3-compatible object storage (Cloudflare R2, AWS S3, MinIO, Backblaze B2) for uploads and cached artwork, set in Admin > Config.

## Run

Download the binary for your system from the [releases page](https://github.com/CarlSinclair/tvt/releases/latest), check it against `SHA256SUMS`, and run it. Nothing else is needed: templates, assets, translations, database migrations and time zone data are all inside the file. Every deployment publishes a new release. To build from source instead:

    cp tvt.env.example tvt.env      # set TVT_JWT_SECRET (32+ chars), TVT_BASE_URL and TVT_ADMIN_EMAIL
    set -a; . ./tvt.env; set +a
    CGO_ENABLED=0 go build -o tvt .
    ./tvt

Sign up with the admin email, then open Admin > Config and add a free TMDB API key (themoviedb.org/settings/api) and, if you want them, mail, storage and the other settings. For local development use `TVT_DEV_LOG_MAIL=1` to print emails to the log (refused when the base URL is https).

Passkeys need a real domain (or `localhost`) because browsers bind them to it; set `TVT_BASE_URL` to the exact public origin.

## Privacy by design

- **No trackers:** no analytics, ads, fonts or embeds from third parties. Telegram's script loads only if you click its button.
- **Artwork never touches the visitor:** TMDB images are fetched by the server on first use, stored, and then served from your own origin or storage, so TMDB never learns who is watching what.
- **Local disk by default, optional S3-compatible bucket (Cloudflare R2, AWS S3, MinIO, Backblaze B2):** with no storage settings, uploads and cached artwork live under `TVT_DATA_DIR/media` and the app serves them. To use a bucket, fill in the Storage group in Admin > Config (endpoint, bucket, access key id, secret access key and the public media URL, which is the bucket's public https domain, for R2 a custom domain; region and prefix are optional); the app refuses to start if only some of the required ones are set. Browsers then fetch images straight from your bucket domain, so image bytes never pass through the app, the application server stays small and disposable, deleting a user's images deletes real objects, and R2 has no egress fees. Everything in the bucket (artwork, profile pictures) is already public on the site and the bucket domain is your own infrastructure, so visitors still never reach a third party. Cached copies of a removed profile image can linger at the CDN for up to a day.
- **Your data is yours:** export and permanent deletion are built in; passwords are hashed, 2FA secrets are encrypted at rest, sessions are stored as hashes.

## Configuration

The environment holds only what the process needs before it can open its database (see `tvt.env.example`):

- `TVT_BASE_URL`: the exact public origin, for example `https://tv.example.com`
- `TVT_JWT_SECRET`: 32 or more random characters
- `TVT_ADMIN_EMAIL`: the verified email that becomes admin
- `TVT_ADDR`, `TVT_DB`, `TVT_DATA_DIR`: listen address, SQLite file and data directory
- `TVT_CLIENT_IP_HEADER`: the header your reverse proxy sets for the client address (`CF-Connecting-IP` behind Cloudflare)
- `TVT_FEDERATION_BLOCK`, `TVT_FEDERATION_ALLOW`: optional host lists for federation
- `TVT_DEV_LOG_MAIL=1`: log sign-in links instead of sending mail (local development only)

Everything else is set in the Admin area: Admin > Config (mail, storage, TMDB/TVDB/Trakt keys, Telegram bot, site name, operator details, contact email, logo, federation), Admin > Servers, Admin > Pages and Admin > FAQ. Secrets are stored encrypted. Changes apply after a restart; the page has a restart button.

## Spoiler rules

A comment is shown as a spoiler if its author tagged it, or if at least one reader reported it and "not a spoiler" reports do not outnumber spoiler reports. Blurred comments carry **Show** and **Not a spoiler** buttons directly on them.

## Security

See `SECURITY.md`. Highlights: hashed refresh tokens with reuse detection, CSRF on every state-changing request, strict CSP, rate limits, forwarded-for only trusted from the local proxy, admin granted only to a verified email, image uploads re-encoded (GIFs rebuilt without their non-image blocks), push endpoints restricted to browser vendors.

## Deploying

`deploy/` has reverse-proxy examples for Caddy, nginx and Apache (`deploy/proxy/`) and a hardened systemd unit. CI (`.github/workflows`) runs tests and `govulncheck` on every pull request; the deploy workflow runs only for the owner, only on a `[deploy]` push to `main`.

## Development

    go vet ./... && go test ./...

## FAQ

### Is federation ActivityPub? Why not support it?

No. TVT federation is ActivityPub's cousin, built for a different goal, and TVT does not
implement ActivityPub. The two share the basic idea: people are addressed as
`name@server`, servers find each other through a well-known document, and servers deliver
signed HTTP POSTs to an inbox. Everything after that differs.

| | TVT federation | ActivityPub |
|---|---|---|
| Default audience | Only servers an admin has allow-listed; friendship decides who sees it in a feed | Public by default; delivery follows followers and public addressing |
| Relationship | Mutual friendship, accepted by both people | One-way follow |
| Data kept elsewhere | Short leases: 14 days for activity, one day for comments (re-confirmed by the home server); a deleted comment answers "gone" | Kept until a Delete arrives, which receivers may ignore |
| Vocabulary | A few small fixed messages (friend request, accept, end, activity, retract, comment, comment withdrawal, rename, report, lease renewal) | JSON-LD activity streams: a large open vocabulary |
| Signing key | One Ed25519 key per server, pinned on first contact | A key per person (usually RSA) fetched from each actor document |
| Fetching | Servers only talk to allow-listed servers and servers where someone has a friend | Servers fetch actors, collections and remote objects from anywhere |
| Spoilers | Spoiler-tagged comments are sent flagged as spoilers and hidden behind the same spoiler cover; episodes are named in activity only to friends who have seen them | Content-warning field exists, but nothing enforces it |

Why it is not supported:

- **Privacy.** TVT shows what people watch. ActivityPub's model assumes publishing, and a
  delete is only a request. A watch history that cannot be reliably withdrawn is a poor fit.
  Leases and friends-only delivery make "it expires on its own" the default.
- **Attack surface.** ActivityPub implementations keep having to defend against signature
  and actor spoofing, server-side request forgery through actor and object fetching, HTML
  injection in remote content, and follow or reply spam from open inboxes. TVT's closed,
  small protocol avoids most of these by not accepting what it does not need.
- **Scope.** TVT needs five messages. Supporting ActivityPub means a JSON-LD processor,
  WebFinger, per-person keys and HTTP Signature variants, shared inboxes, collection paging
  and compatibility with each server's quirks, for little gain on a private feed.

If you want to build an optional ActivityPub bridge anyway, the clean way is a separate
module that only publishes what is already public (for example a creator's profile posts
as `Note` objects, with spoiler flags as content warnings) and accepts replies as
ordinary comments. It should never carry friend activity. Plan for these:

- WebFinger at `/.well-known/webfinger` and a `Person` actor per person.
- Per-person key pairs and HTTP Signatures (draft-cavage and the newer RFC 9421).
- `Follow`, `Accept`, `Undo`, `Create`, `Delete` and `Update` handling.
- Strict sanitizing of remote HTML and a hardened outgoing client like the one in `federation/`.
- Delivery retries with the same outbox, and a way to block servers.

## Roadmap

- More importers: Trakt, Simkl, Letterboxd, IMDb (the TV Time importer is done).
- Operator announcements to all users.
- Translated documentation.

## License

AGPL-3.0-or-later. You may use, modify and host TVT, including commercially, but anyone who runs a modified version for others must offer them its source under the same licence (the footer's "Source code" link, set in Admin > Config) and keep the copyright and attribution notices in [NOTICE](NOTICE).

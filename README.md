# TVT

A free, self-hostable TV show tracker: follow shows, mark episodes watched, see what is next, get notified when new episodes air, and talk about each episode with spoiler protection. One Go binary (templates, assets and 10 languages embedded), SQLite storage, installable PWA.

See it in action at [tvt.carl-sinclair.com](https://tvt.carl-sinclair.com).

Licensed under the [GNU AGPL v3](LICENSE); see [NOTICE](NOTICE) for attribution, [PRIVACY.md](PRIVACY.md) for what the software stores, and [SECURITY.md](SECURITY.md) to report a problem.

## Features

- Exact per-episode air times from TVmaze, then Trakt if configured, where a real broadcast time is known (placeholders are ignored); otherwise the air date at midday in the user's own time zone. Users never have to enter an air time; they only choose how long before or after it to be alerted.
- Second catalog from TheTVDB (optional, `TVT_TVDB_KEY`) for shows TMDB lacks, such as YouTube and web series. TheTVDB requires attribution, which TVT shows on the About page.
- Show catalogue from TMDB: synopses, ratings, posters and per-episode stills, kept fresh by a changes-feed poller; titles and synopses are always stored in each user's own language: every followed show is fetched in the language of each user who follows it, in the background, and refreshed weekly.
- Import from TV Time: upload the GDPR export zip in Settings. Shows are matched through TMDB's TVDB lookup, watched episodes keep their original dates, followed, favorite and saved-for-later shows come across, and nothing you already track is changed. Movies are not imported. The zip is read in memory and not kept.
- Friends: add people by exact username (no fuzzy search), a private friends list, and a home feed of what friends watch, rate and comment on. Spoiler comments never appear, and an episode is only named to a friend who has seen it. Blocked people see nothing. Friendships are addressed by handle so they can later span servers. People can post updates on their own profile; replies are ordinary comments, with the same spoiler flags, reports and moderation.
- Tracking: follow, status, favorites, mark episode / season / everything-up-to-here, ratings, up-next, calendar.
- Episode comments with a spoiler model built around one-click reports, plus abuse reports, blocking and an admin queue.
- Accounts: first factor by password, passkey, magic link or Telegram; optional TOTP two-step verification (with recovery codes) as a second factor on top of any of them.
- Notifications: in-app (live over SSE), Web Push, email and Telegram, with a per-category preference matrix, test buttons, and an alert-timing offset (before or after release, up to 7 days either way).
- Public profile with avatar and banner, favorites, favorite genres and watch-time stats (lifetime, today, 7 days, 30 days).
- Languages: English, Portuguese, Spanish, Italian, French, Chinese, Russian, Japanese, Korean, Arabic (right-to-left).
- Data export and account deletion, a built-in privacy policy page in every language, and no analytics or third-party scripts.
- Optional S3-compatible object storage (Cloudflare R2, AWS S3, MinIO, Backblaze B2) for uploads and cached artwork.

## Run

Download the binary for your system from the [releases page](https://github.com/CarlSinclair/tvt/releases/latest), check it against `SHA256SUMS`, and run it. Nothing else is needed: templates, assets, translations, database migrations and time zone data are all inside the file. Every deployment publishes a new release. To build from source instead:

    cp tvt.env.example tvt.env      # set TVT_JWT_SECRET (32+ chars) and TVT_TMDB_KEY
    set -a; . ./tvt.env; set +a
    CGO_ENABLED=0 go build -o tvt .
    ./tvt

A free TMDB API key is at themoviedb.org/settings/api. For local development use `TVT_DEV_LOG_MAIL=1` to print emails to the log (refused when the base URL is https).

Passkeys need a real domain (or `localhost`) because browsers bind them to it; set `TVT_BASE_URL` to the exact public origin.

## Privacy by design

- **No trackers:** no analytics, ads, fonts or embeds from third parties. Telegram's script loads only if you click its button.
- **Artwork never touches the visitor:** TMDB images are fetched by the server on first use, stored, and then served from your own origin or storage, so TMDB never learns who is watching what.
- **Local disk by default, optional S3-compatible bucket (Cloudflare R2, AWS S3, MinIO, Backblaze B2):** with no `TVT_S3_*` settings, uploads and cached artwork live under `TVT_DATA_DIR/media` and the app serves them. To use a bucket, set `TVT_S3_ENDPOINT`, `TVT_S3_BUCKET`, `TVT_S3_ACCESS_KEY_ID`, `TVT_S3_SECRET_ACCESS_KEY` and `TVT_MEDIA_PUBLIC_URL` (the bucket's public https domain, for R2 a custom domain) together; the app refuses to start if only some are set. Browsers then fetch images straight from your bucket domain, so image bytes never pass through the app, the application server stays small and disposable, deleting a user's images deletes real objects, and R2 has no egress fees. Everything in the bucket (artwork, profile pictures) is already public on the site and the bucket domain is your own infrastructure, so visitors still never reach a third party. Cached copies of a removed profile image can linger at the CDN for up to a day.
- **Your data is yours:** export and permanent deletion are built in; passwords are hashed, 2FA secrets are encrypted at rest, sessions are stored as hashes.

## Configuration

All configuration is environment variables; see `tvt.env.example`. Nothing secret is stored in the repository.

## Spoiler rules

A comment is shown as a spoiler if its author tagged it, or if at least one reader reported it and "not a spoiler" reports do not outnumber spoiler reports. Blurred comments carry **Show** and **Not a spoiler** buttons directly on them.

## Security

See `SECURITY.md`. Highlights: hashed refresh tokens with reuse detection, CSRF on every state-changing request, strict CSP, rate limits, forwarded-for only trusted from the local proxy, admin granted only to a verified email, image uploads re-encoded, push endpoints restricted to browser vendors.

## Deploying

`deploy/` has reverse-proxy examples for Caddy, nginx and Apache (`deploy/proxy/`) and a hardened systemd unit. CI (`.github/workflows`) runs tests and `govulncheck` on every pull request; the deploy workflow runs only for the owner, only on a `[deploy]` push to `main`.

## Development

    go vet ./... && go test ./...

## Roadmap

- Leased replication: opt-in sharing of episode comments between instances without federation's permanence. Each comment is signed by its home instance and copied to peers on a short lease (about a day); peers must re-fetch to renew it, and a deleted comment answers "410 Gone", so deletions propagate within one lease and nothing is immutable. Needs peer allowlists, cross-instance blocking and reporting, and signed identities.
- More importers: Trakt, Simkl, Letterboxd, IMDb (the TV Time importer is done).
- Operator announcements to all users.
- Support for sending messages to Telegram group chats.
- Translated documentation.

## License

AGPL-3.0-or-later. You may use, modify and host TVT, including commercially, but anyone who runs a modified version for others must offer them its source under the same licence (the footer's "Source code" link, set with `TVT_SOURCE_URL`) and keep the copyright and attribution notices in [NOTICE](NOTICE).

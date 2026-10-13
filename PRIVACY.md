# Privacy

TVT is built to hold as little about its users as it can and to keep that data on
the operator's own server. This file describes what the software does; the
running site shows the same policy at `/privacy` in the visitor's language, with
the operator's name and contact filled in from `TVT_OPERATOR_NAME` and
`TVT_PRIVACY_CONTACT`. Operators are responsible for their own legal compliance;
nothing here is legal advice.

## What the software stores

| Data | Why | Notes |
|---|---|---|
| Username, display name, bio, avatar, banner, language, timezone | Account and public profile | Profile images are re-encoded to JPEG (metadata removed) |
| Email address (optional) | Sign-in links, password reset, email notifications | Must be verified before use |
| Password hash (optional) | Sign-in | bcrypt; never stored in plain text |
| Passkey public keys | Sign-in | The private key never leaves the user's device |
| TOTP secret and recovery codes (optional) | Two-step verification | Secret encrypted at rest; recovery codes stored as hashes |
| Telegram ID, handle, chat ID (optional) | Telegram sign-in and notifications | |
| Follows, watched episodes, ratings | The product | |
| Comments, reports, blocks | Discussion and moderation | Deleted comments lose their text immediately. Hidden comments keep their text but are not shown; hiding is undone when the account is restored |
| Notifications, preferences, push subscriptions | Alerts | Notifications are deleted after 90 days |
| Session tokens | Staying signed in | Stored as hashes |
| Previous username | Redirecting to a renamed account and keeping the name from being taken | Kept for 14 days after a change, then removed |
| Email delivery log | Letting an admin see whether mail works | Time, recipient with most of the address hidden (`j***@example.com`), subject (a notification's subject can name a show or episode) and success or error; never the body or sign-in links; only the latest 200 entries are kept |

There are **no analytics, advertising or tracking scripts**, and no third-party
fonts, CDNs or embeds. The only cookie is one essential session cookie (it also holds the CSRF token and short-lived security challenges).

## What leaves the server

- **TMDB, TVmaze, Trakt, TheTVDB:** the server asks them about *shows*. No user data is
  sent. Artwork is fetched by the server once and then served by TVT itself, so a
  visitor's browser never contacts these services and they never see visitor IPs.
- **Telegram:** only if the user chooses it. The Telegram script is loaded on
  click, never on page load.
- **Browser push services and the operator's mail provider:** necessarily see
  the notifications sent through them. Push payloads are encrypted end to end to
  the user's browser.

## Storage: local disk by default, S3 / R2 optionally

Uploads (avatars, banners) and the artwork cache are kept on local disk under
`TVT_DATA_DIR/media` and served by the app, unless the operator configures an
S3-compatible bucket (Cloudflare R2, AWS S3, MinIO, Backblaze B2) together with
its public domain in `TVT_MEDIA_PUBLIC_URL`. With no bucket settings there is no
bucket and none of the rest of this section applies. With a bucket:

- user content is kept off the application server, so a compromised or discarded
  app server does not hold it, and the server stays small and disposable;
- browsers fetch images from the operator's own bucket domain; everything in it
  is already public on the site. A removed profile image may remain in the CDN
  cache for up to a day;
- all artwork requests to TMDB stay server-to-server, which is what protects
  visitors from third parties seeing their IP addresses (TMDB never sees who is
  watching), with or without a bucket;
- deletion is real: removing a user's images removes the objects in the bucket.

## Federation

If an account befriends someone on another TVT server, that server receives the friend
requests and the activity the account shares with friends: episodes watched, ratings,
open (non-spoiler) comments and profile posts, each with show title, episode numbers
and a timestamp. It keeps them for 14 days and drops them sooner when the friendship
ends. Nothing is sent to servers where the account has no friend, spoiler comments are
never sent, and "Share my activity with friends" in the profile settings stops all of
it. Every message is signed by the sending server; the receiving server only accepts
activity from people who are friends with someone on it.

## Deactivating and deleting an account

Whenever an account is deactivated, banned (made read-only) or deleted, by an admin or by
the person, two mutually exclusive switches decide what happens to its comments: **hide**
(kept but not shown, and restored if the account comes back), **delete** (the text is
erased and the comment shows as "Deleted"), or neither (the comments stay as they are).
Replies are never removed. Deleted comments keep their place in the thread.

A person can deactivate their own account in Settings: the profile is hidden, they are
signed out, and signing in again reactivates it and restores hidden comments. When an
account is deleted, its comments are moved to an anonymous "deleted" account (hidden,
erased or kept, as chosen), so other people's replies stay in place. The only things
removed with the account itself are the posts on its own profile, since that profile no
longer exists. Admins have the same switches when they ban, deactivate or delete an
account.

## Imports

An import reads the uploaded export in memory, keeps only the shows, watched episodes and
follow states it needs in a job record while it runs, and deletes that data when it
finishes. Everything else in the export (IP addresses, device data, comments, messages) is
never read or stored. Only TVDB show and episode ids and show titles are sent to TMDB to
find the matching shows.

## What the admin tools show

Admins (the verified `TVT_ADMIN_EMAIL` account) can list accounts with username, display
name, email address and join date; change an account's display name or email; make it
read-only (ban), deactivate or delete it; set a new password or send a reset link; and open
or close sign-ups. They can see recent warnings and errors (kept in
memory only, with addresses already reduced to a country code) and the email delivery log
above. Operational settings such as SMTP, object storage, API keys and the Telegram bot can
also be saved from the admin UI; secrets are stored encrypted and are never shown again.

## What the operator still sees

The operator's reverse proxy, CDN or tunnel (for example Cloudflare) and server
logs can see visitor IP addresses and request paths. TVT itself never writes IP
addresses to its logs: any address is replaced with `[ANONYMIZED_IP]`, and security
events (failed sign-ins, rejected webhooks) record only a country code taken from the
proxy's `CF-IPCountry` header when one is present. IP addresses are used in memory
for rate limiting and are not stored. Set a short retention for the proxy's own logs.

## Operator checklist

1. Set `TVT_OPERATOR_NAME` and `TVT_PRIVACY_CONTACT` so `/privacy` names who is responsible.
2. Keep secrets out of the bucket (it should hold only images); never use `TVT_DEV_LOG_MAIL` in production (it prints sign-in links to the log; the app refuses to start with it on an https site).
3. Decide a log and backup retention period and tell users in the contact details.
4. Keep `TVT_SOURCE_URL` pointing at the exact source you run (AGPL section 13).
5. Handle data requests with the in-app export and delete tools, or directly in the database.

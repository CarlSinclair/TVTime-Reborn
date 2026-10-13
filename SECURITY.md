# Security policy

## Reporting a vulnerability

Please report security problems privately through GitHub's "Report a vulnerability"
button on the Security tab of this repository. Do not open a public issue for
anything exploitable. You will get an acknowledgement within a few days.

## What is in scope

Account takeover, authentication or session bypass, CSRF, injection, privilege
escalation to admin or moderator, leaking of other users' data, and abuse of the
server as a proxy (SSRF) are all in scope.

## Design notes for reviewers

- Passwords are bcrypt-hashed; sessions are short-lived access tokens plus
  rotating, hashed refresh tokens with reuse detection.
- Passkeys (WebAuthn) require user verification and replace the password as the first factor; two-step verification (TOTP) is a separate second factor that is still enforced after a passkey sign-in.
- TOTP secrets and the VAPID private key are encrypted at rest with a key derived
  from `TVT_JWT_SECRET`; rotating that secret requires re-enrolling 2FA.
- All state-changing requests need a CSRF token (double-submit cookie). The only
  exception is the Telegram webhook, which authenticates with a secret header.
- Uploaded import archives are read in memory only: upload size, per-file and total uncompressed size and row counts are capped, only the few expected CSV files are opened, no path from inside the archive is ever used, and imports never overwrite what a user already tracks.
- Federation: servers authenticate with Ed25519 signatures over the method, path, receiving host, a fresh timestamp and a body digest, replays are rejected, keys are pinned on first contact (a changed key is refused until the operator deletes the row in `federation_peers`), outgoing requests refuse private and loopback addresses after DNS resolution, never follow redirects and cap body size, and a server only keeps activity from people who are friends with one of its users.
- Telegram's script is loaded only after the user clicks the Telegram button, and artwork is proxied so visitors never contact third parties.
- Every secret is read from the environment; nothing is committed. If you find a
  credential in this repository or its history, report it.

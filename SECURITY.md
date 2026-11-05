# Security policy

## Reporting a vulnerability

Do not open a public issue for a security problem. Use GitHub's private
vulnerability reporting for this repository from the Security tab. Include the
version, the platform, and the smallest reproduction you can. If private
reporting is unavailable, open a public issue that describes the impact without
exploit details and ask for a private channel.

We aim to acknowledge within three business days and to ship a fix or a
mitigation within thirty days for confirmed issues.

## Scope

- The local API server: loopback binding, the bearer token, and the Host and
  Origin checks.
- The mail connectors and the stored credentials.
- The installer, the pinned runtime download, and the published release assets.
- The embedded UI and its content security policy.

## Design rules

- The API binds IPv4 loopback only and rejects a non-loopback Host or Origin.
- The launch token stays in the URL fragment, moves to sessionStorage, and is
  never written to the DOM, the console or an error message.
- Provider keys use DPAPI on Windows. Other platforms read
  `MAILSENSE_SECRET_<NAME>` from a read-only environment store.
- Logs are redacted before they are written. There is no diagnostics export
  bundle.
- Remote model calls are opt-in. The provider record names the endpoint and the
  model. The [privacy page](docs/privacy.md) lists the request fields and when a
  call runs; the [security page](docs/security.md) states the mail-rendering
  boundary and the limit of a model-written answer.

---
layout: default
title: Contributing
---

# Contributing

<!-- nav:start -->
<div class="doc-index-wrap">
  <nav class="doc-index" aria-label="Documentation">
  <a href="index.html">Documentation</a>
  <a href="ask.html">Ask</a>
  <a href="install.html">Install</a>
  <a href="first-run.html">First run</a>
  <a href="console.html">Console</a>
  <a href="mailboxes.html">Mailboxes</a>
  <a href="providers.html">Providers</a>
  <a href="learning.html">Learning</a>
  <a href="faq.html">FAQ</a>
  <a href="privacy.html">Privacy</a>
  <a href="security.html">Security</a>
  <a href="troubleshooting.html">Troubleshooting</a>
  <span class="doc-index-current" aria-current="page">Contributing</span>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->

Mailsense is Go for the application and plain JavaScript for the interface. It
has no framework on either side. The repository is the source of truth for the
build, the tests and the public pages.

## Start here

[CONTRIBUTING.md](https://github.com/LLM-4-People/mailsense/blob/main/CONTRIBUTING.md)
is the contribution contract. It owns the supported toolchain, the exact command
for each check, the public page rules, the interface rules, the commit format and
the sign-off requirement. This page is the public summary.

Participation is covered by the
[code of conduct](https://github.com/LLM-4-People/mailsense/blob/main/CODE_OF_CONDUCT.md).

## Workflow

Open a pull request against `main`. A change needs a test, a note in the pull
request that says how it was verified, and a `Signed-off-by` line on every
commit. The GitHub web editor adds the sign-off for you; locally use
`git commit -s`.

The pull request checks run the same gates as a release, and the maintainer
reviews the result before merging.

## Report a bug

Include the version, the operating system, the screen or route, what you
expected, what happened, and the relevant Logs entry. Remove mail content,
addresses and provider keys before you paste a log.

Security reports go through the private channel on the
[security page](security.md).

## License

The project is licensed under AGPL-3.0-or-later. By contributing, you agree that
your contribution is distributed under the same license. There is no separate
contributor license agreement.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->

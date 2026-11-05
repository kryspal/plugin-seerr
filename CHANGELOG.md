# Changelog

All notable changes to this plugin are listed here. The version in `VERSION` is the single source of the version number; a published version is never changed.

## 0.1.2

- The two "upcoming" rows are gone: there is nothing to watch yet, so asking for those titles has no use.

## 0.1.1

- Search with spaces works: Seerr wants `%20`, not `+`, in a query (all requests now use `%20`).
- No more false success: specials (season 0) are not offered, since Seerr creates no request for them alone, and an answer from Seerr that holds no request is an error.
- A series stays open to the seasons nobody has asked for, also while other seasons await approval or are processing.
- New optional setting "Address shown to members", used in the link of the "no Seerr account" refusal.

## 0.1.0

- First version: `discover.source` (trending, popular and upcoming rows, search, details with seasons), `work.action` (request a movie, or chosen seasons of a series, as the member's own Seerr user) and `plugin.checks` (test the connection).
- Built with plugin SDK v0.11.0. Needs a server that supports the Discover page.

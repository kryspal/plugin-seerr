# Changelog

All notable changes to this plugin are listed here. The version in `VERSION` is the single source of the version number; a published version is never changed.

## 0.3.3

- The "title" sort is gone: Seerr can only order by the original title, which looks unordered next to translated titles.
- Seerr's locale is looked up while the lists are fetched, not after, and the key's own setting and the default are asked together: a cold first open is up to a second shorter.

## 0.3.2

- Skips the title lookups whenever Seerr already titles the lists in the member's language: it now compares the locale of the key's own user (which Seerr prefers) as well as Seerr's default, and ignores how the language is written (zh-CN, zh-Hans, zh, zh_CN).

## 0.3.1

- A list waits 1.5 seconds (not one) for the title lookups: about 0.2 s more, and far more of the first answer is in the member's language.

## 0.3.0

- Two more filters, original language (Chinese, Japanese, Korean, English, French, German, Spanish; Chinese covers `zh` and `cn`) and a minimum rating (6, 7, 8), genres can be several at once (any of them), and the sort gains "title" (removed again in 0.3.3).
- Faster first opens: lookups run up to twenty at a time (one page in one round trip to TMDB instead of three), a list waits one second for them instead of three (0.3.1 makes it 1.5), and Seerr's own list pages are kept for three minutes.

## 0.2.0

- Follows the revised discover protocol: `DescribeDiscoverSource` (movies and series, genre and period filters, sort), `selections` and `filter_keys` on the popular rows, and `ListMemberWorks` ("my requests", with download progress when Seerr reports it). Option types are renamed (`WorkOption`, `WorkChoice`, `WorkSelection`).
- Genre names for Traditional Chinese are converted from the Simplified names Seerr returns.

## 0.1.4

- The title lookups behind the popular rows are cached per title and language (one day), limited to eight at a time, shared between simultaneous calls, and a call waits at most three seconds for them. A list is back in 4.2 seconds with nothing cached (measured on Seerr v3.5.1) and in 0.03 seconds once cached; titles not looked up in time are filled in in the background.

## 0.1.3

- Popular movies and popular series come back in every language. Seerr reads the language of those two lists as an original-language filter, so a tag such as zh-CN or pt-BR gave an empty list; the plugin now sends no language there and localizes the titles itself.
- A row that fails or is empty is logged (row ID and error, no keys, no member data); the other rows are still shown.
- The member lookup no longer claims to match display names, which Seerr's user search does not cover.

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

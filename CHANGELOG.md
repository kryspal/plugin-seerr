# Changelog

All notable changes to this plugin are listed here. The version in `VERSION` is the single source of the version number; a published version is never changed.

## 0.5.6

- The genre wall heals: when Seerr's genre slider fails (one retry first, logged) or gives genres without pictures, the wall without pictures is kept for 20 seconds instead of ten minutes, so the pictures come back as soon as Seerr can give them.

## 0.5.5

- "Not out yet" is a poster badge now (amber, top left; "Upcoming" in English, which fits the 8-character limit) instead of a caption, on lists and on the details page. The details page keeps the release date as its caption ("Out 2027-03-05"). A title with no date that Seerr calls planned or in production gets the badge without a date.

## 0.5.4

- Titles that are not out yet are marked "Not out yet" in the lists (release date later than today, UTC; a missing or unreadable date is not guessed) and on the details page, which also knows Seerr's production status (Planned, In Production, Post Production) and shows the date. On a person's page the mark follows the role ("Role · Not out yet"). The request button is unchanged.

## 0.5.3

- A member whose Seerr account was deleted no longer takes the whole source offline. Seerr answers 403 to a call made for a user it no longer has; the plugin took that for a refused API key. Now a 403 on a call made for a member drops what is remembered about that member and looks the account up again: none means an empty "my requests" and a refused request with the link to Seerr, and other members and the source are not affected. A refused API key is still reported as one, judged by the lookup made without `X-API-User`.

## 0.5.2

- The Movies and Series tabs are one row each, titled Movies and Series whatever the sort. The page declares the filters (genre, period, language, rating) and the sort (most popular, newest, top rated); the default is most popular.

## 0.5.1

- Studio and network cards are marked as logos (shown whole, not cropped) and use the wider w500 logo; genre cards stay covers.
- A row asked alone carries the page header too (person, studio, network, genre), so a page can say where the row is. Headers never have a backdrop.

## 0.5.0

- Follows the pages protocol (`GetDiscoverPage`): tabs Home, Movies, Series and Genres; a hero row of trending titles; walls of genres, studios and networks as cards; pages for a genre, a studio, a network and a person; recommendations on the details page.
- Details gain the tagline, genres, companies, the director or creators and the cast with their pictures, and trailers as links. Details stay in the member's language; the lists stay in Seerr's.
- The filters and the sort are declared in the page answer. `ListDiscoverRows` and the discover kinds are gone.

## 0.4.0

- Every list (trending, popular movies, popular series, filtered lists) shows the titles, overviews and posters Seerr gives, in Seerr's language setting, so one page has one language. The per-title lookups, the background lookups, the language probe and their caches are gone, and trending sends no `language`. Genre names are as Seerr gives them (no Simplified to Traditional conversion).
- Details, search and the option texts keep the member's language. "My requests" still looks up each title (Seerr's request list has none), with its cache and time limit.
- A cold page takes about 0.3 s (it took about 2.1 s).
- `DescribeDiscoverSource` answers `UNAVAILABLE` when the genre list cannot be fetched and no earlier list exists (one retry first), instead of a description without the genre filter that the host would keep until the plugin is reloaded.

## 0.3.4

- Badges no longer cost a call per title: the status in Seerr's list answers is kept for two minutes and used for the badges of the page; only a title whose status is unknown or deleted (a declined request is possible) or that no recent list showed is asked about. A request made through the plugin voids the remembered status. On a cold page the badges take 0.00 s instead of 0.2 to 0.9 s.
- A list call cancelled by the member (a filter changed) is no longer logged as a failed row.

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

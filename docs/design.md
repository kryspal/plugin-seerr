# Design notes

## Keys and IDs

A work's key is `movie:<TMDB number>` or `tv:<TMDB number>`. The plugin sends only the `tmdb` provider ID in lists, and adds `imdb` (and `tvdb` for series) in the details. The one declared action, `request`, requires `tmdb`.

## Status mapping

Seerr's media status: 1 unknown, 2 pending, 3 processing, 4 partly available, 5 available, 6 blocklisted, 7 deleted.

| Seerr | Work state | Request offered |
| --- | --- | --- |
| 1, 7 or missing | none | yes |
| 2 | pending | no |
| 3 | processing | no |
| 4 | partial | series: the seasons not yet available or requested |
| 5 | available | no |
| 6 | none | no |

Request status: 1 pending, 2 approved, 3 declined, 4 failed. When the media status is 1, 7 or missing, an approved request counts as processing and a pending one as pending. A declined or failed request counts only for the member who made it (found through the Seerr user); the state is then declined or failed and the button reads "Ask again".

4K: Seerr keeps the standard and the 4K version of a title apart (`status` and `status4k`, `seasons` and `seasons4k`, `downloadStatus` and `downloadStatus4k`, and a request has `is4k`). `assess` judges one side: only the requests of that side count and the media status and seasons are that side's. The plugin asks `GET /settings/radarr` (movies) or `/settings/sonarr` (series) and treats the type as having 4K when a server has `is4k` (kept ten minutes; a failed question counts as no 4K for half a minute). Without 4K, only the standard side exists, as before. With 4K, a title shows the better of the two states (available, partial, processing, pending, declined or failed, none), can be requested while either side can, and the offer gets a `quality` question (single choice, `standard` or `4k`, required) before the seasons; a side that cannot be requested is disabled with the reason as its note. A season is open when it is open on either side, and the side chosen is checked again when the request is made (a refusal otherwise). The request body carries `is4k` only when the type has a 4K server. A member's own request in the list uses the side its `is4k` names.

For a series, a season is blocked when Seerr lists it as pending, processing, partly available or available, or when an open request names it. Specials (season 0) are not offered: Seerr creates no request for them alone. A series stays open to every season that is not blocked, whatever its overall state.

## Calls

- Badges (`include_actions` false): the status each list answer (discover, trending, search) carried in `mediaInfo.status` is kept for two minutes, so the badges of a page of works need no call per title. A title shown without `mediaInfo` has no record in Seerr, so it is simply not requested. What the list does not carry is the requests (a declined or failed request counts only for the member who made it), so a title whose status is unknown or deleted, or one no recent list showed, is asked about with one `GET /movie/{id}` or `/tv/{id}`, many at a time, kept for ten seconds. A request made through the plugin makes the remembered status of that title void. No user lookup unless a title has a declined or failed request.
- Opening a title (`include_actions` true): the same call without the cache, and the offer with the season question.
- Request: user lookup (cached five minutes per member code), a fresh detail check, `POST /request` with `X-Api-Key` and `X-API-User`, a fresh detail again for the new state.
- Refusals, not errors: no Seerr account (with a link), 403 with "quota" in the message, any other 403, 409, nothing left to request, no valid season. A rejected API key (401) is `FAILED_PRECONDITION`, an unreachable Seerr is `UNAVAILABLE`.

## Languages

Seerr v3.5.1: `/discover/movies` and `/discover/tv` use the query's `language` as `with_original_language` (a tag with a region such as `zh-CN` matches nothing, `zh` returns Chinese-language titles only) and title the list in Seerr's own locale (the Seerr user's setting, else the server default). `/discover/trending` without `language` does the same (checked with the default locale en and zh-CN, also with `X-API-User`). `/search` and `/movie|tv/{id}` use `language` as the display language. Decision (0.4.0): every list shows Seerr's text, so a page has one language; the plugin sends no `language` to trending or the popular lists (popular sends `language=all`, or the chosen original-language codes), looks nothing up, and converts nothing. Details and search keep the member's language; the option texts too. `ListMemberWorks` is the exception that looks titles up (`/movie|tv/{id}?language=`, cached a day, twenty at a time, six seconds per page) because Seerr's request list has no titles; that is not a translation.

## Where the time goes (measured, Seerr v3.5.1 in a container on the same host)

TMDB answers in about 0.5 s whatever Seerr asks, so a Seerr call that is not cached is 0.3 to 0.9 s. A list page is one such call per row, the three rows run together, and Seerr's list pages are kept for three minutes by the plugin.

Measured on 0.4.0 (Seerr v3.5.1 in a container on the same host, member through `X-API-User`, three rows fetched together, pages never opened before, three each): Seerr default language en 0.29 to 0.30 s, zh-CN 0.35 to 0.36 s. The badges cost nothing (the status comes with the list). Before 0.4.0 the same cold page took 2.1 s because of the title lookups.

## Pages

Protocol: `DescribeDiscoverSource` (tabs, search types), `GetDiscoverPage(ref, row_id, page_token, selections)`, `SearchWorks`, `GetWork`. Refs: `home`, `movies`, `series`, `genres`, `genre:<movie|tv>:<id>`, `studio:<id>`, `network:<id>`, `person:<id>`, `rec:<movie|tv>:<id>`; anything else is NOT_FOUND.

- Home: hero (first ten trending titles with a backdrop; the trending row shows the rest of that same Seerr page, one call), popular movies and series, cards for 18 studios and 16 networks (`walls.go`; IDs and logos checked against Seerr v3.5.1).
- Movies / Series: the popular list; the filters (genre, period, original language, rating) and the sort are declared on the page and on a row asked alone, and do not depend on the selection. The genre list failing with no earlier list is UNAVAILABLE.
- Genres: two card rows from `/discover/genreslider/movie|tv` (names in the member language, the fifth backdrop, or the first, as the card image in the genre's two Seerr colours through TMDB's duotone filter, like Seerr's own wall; falls back to `/genres` without pictures). A genre page uses `/discover/movies|tv?genre=`; its genre name must be in the genre list, else NOT_FOUND.
- Studio: header from `/studio/{id}`, list from `/discover/movies?studio=`. Network: `/network/{id}`, `/discover/tv?network=`. Seerr has no endpoint that lists studios or networks. Series production companies are text only, because studio pages list movies.
- Person: header from `/person/{id}?language=` (member language), works from `/person/{id}/combined_credits`, which has no paging and is in Seerr's language: cached five minutes, sorted by date (newest first, undated last, stable), cut into pages of 20 by offset. Cast only, with TV entries of genre talk (10767) or news (10763) dropped unless nothing else is left; with no cast, the crew entries, one per work with the jobs joined. Seerr answers 500 for an unknown person and the HTTP client drops the body of a 5xx, so a 500 on a person call is read as NOT_FOUND.
- Recommendations: `/movie|tv/{id}/recommendations` (not `similar`, which is unbounded and poor). Badges: the status in a list answer is remembered as before.
- Details: tagline, genres (ref to the genre page), companies (networks first), credits (director(s) or creators first, then the cast in TMDB order, 30 at most), related_ref, and up to two https trailers as external links. Lists carry none of it and cost no extra call.

## Seeds (discover.seeds) and the "For you" tab

The manifest declares the permission `discover.seeds`, `discover_seed_ids: ["tmdb"]` and a last tab `foryou` with `seeded: true` (the first tab cannot be seeded). The host sends seeds (library: up to 20, watched: up to 30, no order) only with requests for that tab, including its row requests; Home and the other pages never get them. The tab has:

- Row order: `for_you` first, then the collections.
- `collection_1` to `collection_3`: the whole collection of a movie seed that belongs to one (movie details `collection`, then `/collection/{id}`), oldest first, the watched seeds first and the library's after them, each collection once, a collection of one left out. These rows are not thinned by the one-work-once rule so that a collection stays whole.
- `for_you` (refreshable, title "For you", no film named): the batch number is the random seed. A batch takes four of the watched seeds (the library's when nothing was watched; the pool is sorted first because the host gives no order, so one batch always means the same row), and from each a random page of `/movie|tv/{id}/recommendations` (the first page tells how many pages there are; at most ten deep), shuffled, without the seeds themselves and without titles that have no poster, ten from each, taken in turn to mix them, forty at most. Every batch number is a valid batch, including the largest.
- Without seeds, or with seeds without a TMDB number, the page is empty. A seed Seerr does not know gives nothing; if none is known the row is empty, not an error. Filters and sort do not depend on the seeds. Caches hold Seerr's answers by TMDB number (title and collection per work, 30 minutes; collection parts, 10 minutes; recommendation pages in the 3-minute list cache), never the set of seeds.

Measured on Seerr v3.5.1 in a container with 30 watched and 20 library seeds (the most popular titles of Seerr's own lists): the tab takes 2.45 s the first time (up to thirty detail calls, three collection calls and the recommendation pages, all together) and 0.37 s when Seerr has them warm; one batch asked alone takes at most 0.53 s. Over 40 batches one after another, 1589 works were shown, 1224 distinct (23 percent repeats, 1 percent between neighbouring batches). Of the 22 watched movie seeds, 6 belong to a collection.

## Filters and sort

The Movies and Series tabs declare, in the page answer (and again on a row asked alone): a multiple-choice `genre` filter (any of the chosen: `|`), a single-choice `period` filter (2020s ... 1980s, before 1980), an original-language filter (`language`: zh, ja, ko, en, fr, de, es), a minimum-rating filter (`rating`: 6, 7, 8) and a `sort` (popular, newest, top rated) as the page's sort. The pages of a genre, studio or network declare the same without the genre filter. Home and the Genres tab declare none.

Selections become `/discover/movies|tv` parameters (tested on Seerr v3.5.1: `language=zh|cn` returns Mandarin and Cantonese titles together, `zh` alone and `cn` alone each only their own, a comma in `language` or `genre` must be percent-encoded and means and, `|` means or; when no language is chosen the plugin sends `language=all` so a default original language set in Seerr does not narrow the list): `genre` (a number, checked again), `primaryReleaseDateGte/Lte` (movies) or `firstAirDateGte/Lte` (series), `sortBy` (`primary_release_date.desc` / `first_air_date.desc` for newest, `vote_average.desc` for top rated) `voteAverageGte` with a vote floor (200 movies, 100 series) for the rating filter, and `voteCountGte` (500 movies, 200 series) for top rated. Newest always gets an upper date of today (or the period's end if that is earlier), since Seerr otherwise sorts titles not yet released first. Unknown keys and values are ignored; the sort values are a whitelist here too. The page token is Seerr's page number and is sent with the same selections.

## My requests

`ListMemberWorks` asks `GET /request?sort=modified&sortDirection=desc&take=20&skip=N` with `X-API-User` set to the member's Seerr user. On v3.5.1 Seerr then returns only that user's requests; the plugin never sends `requestedBy` and the page token holds only the offset. A member with no Seerr account gets an empty list. Seerr's answer has no titles, so they come from the same cached, bounded lookups as the popular rows (waiting at most six seconds; a request whose title is not ready is listed as "Untitled" and has its title on the next load). The state is the same mapping as for badges. Only for requests in processing, from `media.downloadStatus` (`downloadStatus4k` for a 4K request; source reading of v3.5.1, not tried against Radarr or Sonarr): the items are first made one per download, because a season pack is listed once per episode with the same `downloadId` and the pack's size (items without an id are told apart by title and size, items with neither all count). Progress is done over total of those, and none while a download has no known size. The stage comes from the queue status Seerr passes on: `downloading`, `queued` and `delay` (queued), `paused`, `completed` (importing, the files are in), `warning`, `failed` and `downloadClientUnavailable` (stalled); other values are left out. Of several downloads the one that needs attention most is given: stalled, then paused, then the earliest of queued, downloading, importing. The expected finish is the latest `estimatedCompletionTime` as RFC 3339 UTC; it is left out when a download still going has none (a finished one waiting for import does not count) or the value is not a time or is Seerr's empty one (1970).

## Not done

- No sort by title: Seerr orders only by the original title, so a page would look unordered next to translated titles, and paging cannot re-sort by the displayed name.

- No server or quality profile choice (only standard or 4K), no cancelling or approving requests: those stay in Seerr.
- Search cannot be filtered by type in Seerr, so a page filtered to one type may hold fewer than 20 titles.

## Notes for authors of other sources

This connector is meant to be read as an example of the pages protocol. What it shows, and what a source of your own can do with the same pieces:

- Tabs are pages with a ref of your own; a tab with one row and declared filters and sort is a filterable grid (Movies, Series here).
- Walls of entry cards (`CARDS`): cover pictures for things like genres, `LOGO` for logos that must be shown whole (studios, networks here). A card with `PORTRAIT` is for people: a source that lists people (an actor wall of one tab, one `CARDS` row, its own filters and sort) can use it. Seerr has no endpoint that lists people (checked on v3.5.1: no popular or trending people; `/search` finds people by name only), so this connector has no actor wall; a person is reached from a title's credits.
- A header gives a page its title, a small picture, text and short facts, and never needs a backdrop. A row asked alone carries the header too.
- Keep lists in one language (the upstream's own) and put the member's language in labels and details.
- Slow rows can be sent empty with a page token so that they load when they come into view; keep a short cache of your own, because the host does not cache page answers.
- A ref is untrusted input: parse it strictly and answer NOT_FOUND for anything that does not resolve.
- A landscape row (`DISCOVER_ROW_STYLE_LANDSCAPE`) shows each work by its `backdrop_url`, in the proportions the row declares; a work without a backdrop gets a placeholder, never its poster. Still give `poster_url` too: a host that does not know the style lays the row out as posters. A row where no work has a backdrop fails the conformance suite. This connector shows posters only: Seerr lists have posters and backdrops, but its rows are tuned for posters.
- `Work.product_code` is an optional plain identification number for a work (trimmed, at most 64 characters; the host drops one with control characters). Fill it where the upstream has such a number and a viewer would look the work up by it; the source only supplies the value and does not judge the library. Lists and `GetWork` carry their own, so fill it in `GetWork` too if the detail page should show it. Seerr has no such number, so this connector leaves it empty.

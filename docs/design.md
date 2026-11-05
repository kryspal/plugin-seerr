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

Request status: 1 pending, 2 approved, 3 declined, 4 failed. When the media status is 1, 7 or missing, an approved request counts as processing and a pending one as pending. A declined or failed request counts only for the member who made it (found through the Seerr user); the state is then declined or failed and the button reads "Ask again". 4K requests are ignored.

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

## Filters and sort

`DescribeDiscoverSource` declares, for movies and for series: a multiple-choice `genre` filter (any of the chosen: `|`) (Seerr's `/genres/movie|tv`, kept ten minutes, the last list kept when Seerr is down, `UNAVAILABLE` when there never was one, so the host does not keep a description without the filter; an empty list from Seerr only leaves the filter out), a single-choice `period` filter (2020s ... 1980s, before 1980), an original-language filter (`language`: zh, ja, ko, en, fr, de, es), a minimum-rating filter (`rating`: 6, 7, 8) and a `sort` (popular, newest, top rated). The popular rows list genre, period, language, rating and sort in `filter_keys`; trending lists none.

Selections become `/discover/movies|tv` parameters (tested on Seerr v3.5.1: `language=zh|cn` returns Mandarin and Cantonese titles together, `zh` alone and `cn` alone each only their own, a comma in `language` or `genre` must be percent-encoded and means and, `|` means or; when no language is chosen the plugin sends `language=all` so a default original language set in Seerr does not narrow the list): `genre` (a number, checked again), `primaryReleaseDateGte/Lte` (movies) or `firstAirDateGte/Lte` (series), `sortBy` (`primary_release_date.desc` / `first_air_date.desc` for newest, `vote_average.desc` for top rated) `voteAverageGte` with a vote floor (200 movies, 100 series) for the rating filter, and `voteCountGte` (500 movies, 200 series) for top rated. Newest always gets an upper date of today (or the period's end if that is earlier), since Seerr otherwise sorts titles not yet released first. Unknown keys and values are ignored; the sort values are a whitelist here too. The page token is Seerr's page number and is sent with the same selections.

## My requests

`ListMemberWorks` asks `GET /request?sort=modified&sortDirection=desc&take=20&skip=N` with `X-API-User` set to the member's Seerr user. On v3.5.1 Seerr then returns only that user's requests; the plugin never sends `requestedBy` and the page token holds only the offset. A member with no Seerr account gets an empty list. Seerr's answer has no titles, so they come from the same cached, bounded lookups as the popular rows (waiting at most six seconds; a request whose title is not ready is listed as "Untitled" and has its title on the next load). The state is the same mapping as for badges. Progress is the sum over `media.downloadStatus` of done over total, shown only for requests in processing and only when Seerr lists a download (source reading of v3.5.1; not tried against Radarr or Sonarr).

## Not done

- No sort by title: Seerr orders only by the original title, so a page would look unordered next to translated titles, and paging cannot re-sort by the displayed name.

- No 4K requests, no server or quality profile choice, no cancelling or approving requests: those stay in Seerr.
- Search cannot be filtered by type in Seerr, so a page filtered to one type may hold fewer than 20 titles.

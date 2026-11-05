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

Seerr v3.5.1: `/discover/movies` and `/discover/tv` use the query's `language` as `with_original_language` (a tag with a region such as `zh-CN` matches nothing, `zh` returns Chinese-language titles only) and title the list in the locale of the Seerr user (`X-API-User`, else the server default). `/discover/trending`, `/search` and `/movie|tv/{id}` use `language` as the display language. So the popular rows send no language and, when the locale Seerr uses for the plugin's key (the key's own user setting `GET /user/{id}/settings/main`, else the default `GET /settings/main`; cached five minutes; written the way the member's language is, so zh-Hans, zh and zh_CN are one) is not the member's language, look each title up with `/movie|tv/{id}?language=` (Seerr caches TMDB answers). The lookups are kept per title and language for a day, run at most twenty at a time (one page), one per title and language even when several calls ask at once, and have their own 30-second deadline. A call waits at most 1.5 seconds for them: titles not found by then keep Seerr's text for this call and are filled in by the lookups that go on in the background, so the next call has them. Measured against Seerr v3.5.1 on the same host (all three rows, 60 titles, zh-TW): 4.2 s with nothing cached, 3.2 s while the first lookups are still running, 0.03 s once cached; one row page 2.1 to 3.3 s; no call took longer than 4.2 s.

## Where the time goes (measured, Seerr v3.5.1 in a container on the same host)

TMDB answers in about 0.5 s whatever Seerr asks (a bare request to api.themoviedb.org takes 0.48 to 0.53 s), so every Seerr call that is not cached is 0.3 to 0.9 s. Seerr itself does not serialize: 20 uncached `/movie/{id}?language=` calls take 10.2 s one after the other, 2.9 s eight at a time, 1.5 s all at once. A page of 20 titles therefore costs one round trip to TMDB when all 20 run together, three rounds with eight slots, which is why the lookup slots are 20 and not 8. The list itself (`/discover/...`) is one more call of 0.5 to 0.8 s when uncached; Seerr's list pages are kept for three minutes. With a one-second budget for the lookups an uncached page returns in 1.5 to 1.8 s; with 1.5 s (the one used) in 1.7 to 2.0 s; with 3 s in 3.2 to 3.5 s. The titles localized in the first answer (zh-TW and zh-CN, pages never opened before): budget 1 s, 0 to 12 of 20 (movies 1 and 12, series 0 and 6); budget 1.5 s, 18 to 20 of movies and 2 to 11 of series; budget 3 s, movies 19 and series 8 to 16. The rest arrive through the background lookups a few seconds later (movies 18 to 20 of 20, series 8 to 18 of 20; the others have no title in that language) and are there on the next call.

Measured segments of one cold page of 20 (Seerr v3.5.1 in a container on the same host, Seerr default language en, member zh-CN and zh-TW, mean of three pages each), host view = list call, then badges: Seerr list 0.45 to 0.71 s; list call 2.07 to 2.14 s, of which the title lookups wait about 1.4 to 1.6 s (the 1.5 s budget); badges 0.00 s (0.3.3: 0.24 s for series, 0.83 to 0.90 s for movies); total 2.07 to 2.14 s (0.3.3: 2.32 to 3.04 s).

## Filters and sort

`DescribeDiscoverSource` declares, for movies and for series: a multiple-choice `genre` filter (any of the chosen: `|`) (Seerr's `/genres/movie|tv`, kept ten minutes, the last list kept when Seerr is down, the filter left out when there never was one), a single-choice `period` filter (2020s ... 1980s, before 1980), an original-language filter (`language`: zh, ja, ko, en, fr, de, es), a minimum-rating filter (`rating`: 6, 7, 8) and a `sort` (popular, newest, top rated). The popular rows list genre, period, language, rating and sort in `filter_keys`; trending lists none.

Selections become `/discover/movies|tv` parameters (tested on Seerr v3.5.1: `language=zh|cn` returns Mandarin and Cantonese titles together, `zh` alone and `cn` alone each only their own, a comma in `language` or `genre` must be percent-encoded and means and, `|` means or; when no language is chosen the plugin sends `language=all` so a default original language set in Seerr does not narrow the list): `genre` (a number, checked again), `primaryReleaseDateGte/Lte` (movies) or `firstAirDateGte/Lte` (series), `sortBy` (`primary_release_date.desc` / `first_air_date.desc` for newest, `vote_average.desc` for top rated) `voteAverageGte` with a vote floor (200 movies, 100 series) for the rating filter, and `voteCountGte` (500 movies, 200 series) for top rated. Newest always gets an upper date of today (or the period's end if that is earlier), since Seerr otherwise sorts titles not yet released first. Unknown keys and values are ignored; the sort values are a whitelist here too. The page token is Seerr's page number and is sent with the same selections.

## My requests

`ListMemberWorks` asks `GET /request?sort=modified&sortDirection=desc&take=20&skip=N` with `X-API-User` set to the member's Seerr user. On v3.5.1 Seerr then returns only that user's requests; the plugin never sends `requestedBy` and the page token holds only the offset. A member with no Seerr account gets an empty list. Seerr's answer has no titles, so they come from the same cached, bounded lookups as the popular rows (waiting at most six seconds; a request whose title is not ready is listed as "Untitled" and has its title on the next load). The state is the same mapping as for badges. Progress is the sum over `media.downloadStatus` of done over total, shown only for requests in processing and only when Seerr lists a download (source reading of v3.5.1; not tried against Radarr or Sonarr).

## Not done

- No sort by title: Seerr orders only by the original title, so a page would look unordered next to translated titles, and paging cannot re-sort by the displayed name.

- No 4K requests, no server or quality profile choice, no cancelling or approving requests: those stay in Seerr.
- Search cannot be filtered by type in Seerr, so a page filtered to one type may hold fewer than 20 titles.

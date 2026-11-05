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

- Badges (`include_actions` false): one `GET /movie/{id}` or `/tv/{id}` per title, at most six at a time, kept for ten seconds. No user lookup unless a title has a declined or failed request.
- Opening a title (`include_actions` true): the same call without the cache, and the offer with the season question.
- Request: user lookup (cached five minutes per member code), a fresh detail check, `POST /request` with `X-Api-Key` and `X-API-User`, a fresh detail again for the new state.
- Refusals, not errors: no Seerr account (with a link), 403 with "quota" in the message, any other 403, 409, nothing left to request, no valid season. A rejected API key (401) is `FAILED_PRECONDITION`, an unreachable Seerr is `UNAVAILABLE`.

## Languages

Seerr v3.5.1: `/discover/movies` and `/discover/tv` use the query's `language` as `with_original_language` (a tag with a region such as `zh-CN` matches nothing, `zh` returns Chinese-language titles only) and title the list in the locale of the Seerr user (`X-API-User`, else the server default). `/discover/trending`, `/search` and `/movie|tv/{id}` use `language` as the display language. So the popular rows send no language and, when Seerr's default locale (`GET /settings/main`, cached five minutes) is not the member's language, look each title up with `/movie|tv/{id}?language=` (Seerr caches TMDB answers). The lookups are kept per title and language for a day, run at most eight at a time, one per title and language even when several calls ask at once, and have their own 30-second deadline. A call waits at most three seconds for them: titles not found by then keep Seerr's text for this call and are filled in by the lookups that go on in the background, so the next call has them. Measured against Seerr v3.5.1 on the same host (all three rows, 60 titles, zh-TW): 4.2 s with nothing cached, 3.2 s while the first lookups are still running, 0.03 s once cached; one row page 2.1 to 3.3 s; no call took longer than 4.2 s.

## Filters and sort

`DescribeDiscoverSource` declares, for movies and for series: a single-choice `genre` filter (Seerr's `/genres/movie|tv`, kept ten minutes, the last list kept when Seerr is down, the filter left out when there never was one), a single-choice `period` filter (2020s ... 1980s, before 1980) and a `sort` (popular, newest, top rated). The popular rows list all three in `filter_keys`; trending lists none.

Selections become `/discover/movies|tv` parameters: `genre` (a number, checked again), `primaryReleaseDateGte/Lte` (movies) or `firstAirDateGte/Lte` (series), `sortBy` (`primary_release_date.desc` / `first_air_date.desc` for newest, `vote_average.desc` for top rated) and `voteCountGte` (500 movies, 200 series) for top rated. Newest always gets an upper date of today (or the period's end if that is earlier), since Seerr otherwise sorts titles not yet released first. Unknown keys and values are ignored; the sort values are a whitelist here too. The page token is Seerr's page number and is sent with the same selections.

## My requests

`ListMemberWorks` asks `GET /request?sort=modified&sortDirection=desc&take=20&skip=N` with `X-API-User` set to the member's Seerr user. On v3.5.1 Seerr then returns only that user's requests; the plugin never sends `requestedBy` and the page token holds only the offset. A member with no Seerr account gets an empty list. Seerr's answer has no titles, so they come from the same cached, bounded lookups as the popular rows (waiting at most six seconds; a request whose title is not ready is listed as "Untitled" and has its title on the next load). The state is the same mapping as for badges. Progress is the sum over `media.downloadStatus` of done over total, shown only for requests in processing and only when Seerr lists a download (source reading of v3.5.1; not tried against Radarr or Sonarr).

## Not done

- No 4K requests, no server or quality profile choice, no cancelling or approving requests: those stay in Seerr.
- Search cannot be filtered by type in Seerr, so a page filtered to one type may hold fewer than 20 titles.

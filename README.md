# plugin-seerr

Connects a [Kryspal Media](https://github.com/kryspal) server to your own [Seerr](https://github.com/seerr-team/seerr) server. Members browse what Seerr offers on the server's Discover page and ask for a title with one button; Seerr keeps the requests, the approvals and the quotas. Built on [plugin-sdk](https://github.com/kryspal/plugin-sdk).

```sh
go build -o seerr .
go vet ./... && go test ./...
```

## What it does

- **Discover** (`discover.source`): four tabs. *Home* has a large recommendation row from what is trending, the trending list, popular movies and series, and walls of well-known studios and networks. *Movies* and *Series* show the popular list with the filters below. *Genres* is a wall of genre cards. Genres, studios, networks and people are pages you can open from the tabs and from a title's details; a person's page lists their works, newest first (appearances on talk shows and news are left out, and someone with no acting shows what they made). A title's details page has the tagline, genres, companies (the network first), the director or creators and the cast, the trailers as links that open the video site in a new tab, and what Seerr recommends after it. Search is also here. All data comes from Seerr's own API. The wall of studios and networks is a fixed list kept in the plugin, because Seerr cannot list them.
- **For you** (optional, off until the administrator allows it on the plugin's page): a tab that the server fills with a few titles of its library and titles the member watched. It shows a row of recommendations after a few of the watched titles, drawn at random (the viewer can ask for another batch), and below it the rest of the collections they belong to ("Complete the collection", the watched ones first). The row's title names no film. Only TMDB numbers are used, for the one request, and nothing is written to disk; Seerr is asked about each number and the answers are kept in memory for a limited time. Without the permission, or with nothing that fits, the tab is empty and the server does not show it.
- **Filters**: the Movies and Series tabs, and the pages of a genre, studio or network, can be filtered by genre (several at once: any of them), period (decades, and before 1980), original language (Chinese, Japanese, Korean, English, French, German, Spanish) and rating (6, 7 or 8 and up), and sorted by popularity, newest or top rated. "Newest" never lists titles that are not out yet; "top rated" and the rating filter ignore titles with few votes. There is no sort by title: Seerr can only order by the original title, which looks unordered next to translated titles. Chinese covers both Mandarin (zh) and Cantonese (cn) titles. Genre names are the ones Seerr gives in the member's language. Trending cannot be filtered.
- **My requests** (`ListMemberWorks`): the member's own requests, last changed first, with Seerr's state and, when Seerr is connected to a download manager and reports it, the download progress. Without one there is only the state.
- **Request** (`work.action`): a button on titles that have a TMDB number. For a series the member chooses the seasons; seasons that are already available or requested are shown but cannot be chosen, and the seasons nobody has asked for stay open, so a series can be requested in steps. Specials (season 0) are not offered: Seerr does not create a request for them alone.
- **State**: each title shows what Seerr says: awaiting approval, processing, partly available, available, declined or failed. A title that is in your library always shows as in the library, whatever Seerr says.
- **Test connection** (`plugin.checks`): a button on the settings page that checks the address and the API key.

The plugin keeps no state. Seerr is the only place requests live.

## Settings

| Setting | |
| --- | --- |
| Seerr address | How this server reaches Seerr, for example `http://192.168.1.20:5055`. A path prefix behind a reverse proxy is fine. |
| Seerr API key | Seerr, Settings, General. |
| Address shown to members | Optional. The address the server uses to reach Seerr (for example a Docker network name) may not open in a member's browser. When a member has no Seerr account yet, the refusal carries a link; it points to this address when you set it, otherwise to the one above. |

The plugin declares the permission `network.lan`, because Seerr usually runs on the local network.

After Kryspal Media creates a new media library, refresh the library list in Seerr's settings and enable the new library; until then Seerr does not mark requests for it as available.

## Who the request is made as

A request made with only the API key would be made, approved and counted as the administrator who owns the key. The plugin sends the header `X-API-User` with the member's Seerr user ID, so Seerr applies that member's own permissions, approval rules and quota.

The member is found by sign-in name with Seerr's user search, trying in order: the Jellyfin or Emby sign-in name, the Seerr user name and the Plex name (Seerr's user search does not cover display names). If there is no match, or two users share the name, the member is told to sign in to Seerr once with their Kryspal Media account, with a link to Seerr. The plugin never creates Seerr users.

`X-API-User` is not in Seerr's published API description. The tests pin the behavior; it is known to work with Seerr v3.5.1 and newer.

## Languages

The language of the lists follows Seerr's language setting (Settings, General; or the language in the profile of the administrator whose API key the plugin uses, if that profile has one): trending, popular movies, popular series and every filtered list show the titles, overviews and posters exactly as Seerr gives them, so one page is in one language. The plugin does not translate them or look them up again. Set Seerr's language to the one most members read. The details of a title and the search results follow the member's own language, and so do the names of the options (period, language, rating, sort). Genre names are as Seerr gives them in the member's language. Seerr v3.5.1 reads the `language` parameter of its popular lists as an original-language filter, so the plugin sends none there (only the original-language choice of the member). "My requests" asks Seerr for each title's name because Seerr's request list has none; those names are cached for a day and a page waits at most six seconds for them. A list that fails or comes back empty is logged with its row ID. Seerr's own pages are kept for three minutes, so paging back and forth does not ask Seerr again.

## Images

Seerr lists posters and backdrops by their TMDB image path. The server downloads them through its image cache; viewers never connect to the image host.

## Privacy

The server passes a member's sign-in name and an opaque member code to this plugin so it can find the member's Seerr user. The plugin does not log the name and keeps the match in memory for five minutes.

Implementation notes: [docs/design.md](docs/design.md).

Licensed under the Apache License 2.0 (see `LICENSE` and `NOTICE`). Seerr is a separate project; this plugin is not made or endorsed by it.

# plugin-seerr

Connects a [Kryspal Media](https://github.com/kryspal) server to your own [Seerr](https://github.com/seerr-team/seerr) server. Members browse what Seerr offers on the server's Discover page and ask for a title with one button; Seerr keeps the requests, the approvals and the quotas. Built on [plugin-sdk](https://github.com/kryspal/plugin-sdk).

```sh
go build -o seerr .
go vet ./... && go test ./...
```

## What it does

- **Discover** (`discover.source`): rows for trending titles, popular movies and popular series; search; and a details page with the seasons of a series. All data comes from Seerr's own API. Kryspal Media does not call any other metadata service for this and stores no key for one.
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

## Who the request is made as

A request made with only the API key would be made, approved and counted as the administrator who owns the key. The plugin sends the header `X-API-User` with the member's Seerr user ID, so Seerr applies that member's own permissions, approval rules and quota.

The member is found by sign-in name with Seerr's user search, trying in order: the Jellyfin or Emby sign-in name, the Seerr user name and the Plex name (Seerr's user search does not cover display names). If there is no match, or two users share the name, the member is told to sign in to Seerr once with their Kryspal Media account, with a link to Seerr. The plugin never creates Seerr users.

`X-API-User` is not in Seerr's published API description. The tests pin the behavior; it is known to work with Seerr v3.5.1 and newer.

## Languages

Titles, overviews and posters follow the member's language, whichever language that is, including Traditional Chinese (zh-TW), and regional tags such as pt-BR or en-GB. Seerr v3.5.1 reads the `language` parameter of its popular movies and popular series lists as an original-language filter (and answers with an empty list for a tag with a region), and titles those lists in the Seerr user's own locale. The plugin therefore does not send a language there and looks the titles up in the member's language afterwards when Seerr's locale differs. The lookups are cached for a day, run a few at a time, and a list never waits more than three seconds for them: the first time a list is opened some titles may still be in Seerr's own language, and the next time they are all in the member's. A list that fails or comes back empty is logged with its row ID.

## Images

Seerr lists posters and backdrops by their TMDB image path. The server downloads them through its image cache; viewers never connect to the image host.

## Privacy

The server passes a member's sign-in name and an opaque member code to this plugin so it can find the member's Seerr user. The plugin does not log the name and keeps the match in memory for five minutes.

Implementation notes: [docs/design.md](docs/design.md).

Licensed under the Apache License 2.0 (see `LICENSE` and `NOTICE`). Seerr is a separate project; this plugin is not made or endorsed by it.

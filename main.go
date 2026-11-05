// Command seerr is the Kryspal Media connector for a Seerr server (capabilities discover.source, work.action and plugin.checks).
//
// It shows what Seerr offers (trending and popular titles, search, details) on the server's Discover page, and lets a member ask
// for a title through Seerr's own request flow. Seerr keeps the requests, the approvals and the quotas; the plugin keeps no state.
package main

import "github.com/kryspal/plugin-sdk/sdk"

func main() {
	p := newPlugin()
	sdk.Main(sdk.Config{Plugin: p, DiscoverSource: p, WorkActionProvider: p, Checker: p})
}

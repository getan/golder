package judge

import "sync/atomic"

// networkFollow is the opt-in /network follow policy. It is process state rather
// than a per-gate field so a mid-session toggle reaches every gate already
// installed in the session; the CLI loads the persisted value when it builds
// the gates.
var networkFollow atomic.Bool

// SetNetworkFollow turns the policy on or off.
func SetNetworkFollow(on bool) { networkFollow.Store(on) }

// NetworkFollow reports whether an auto-mode Allow carries the network bit.
func NetworkFollow() bool { return networkFollow.Load() }

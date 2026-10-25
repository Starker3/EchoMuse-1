package wifi

import (
	"log"
	"time"
)

// DHCP stall recovery where the firmware runs DHCP itself (ownsDHCP): the
// Echo Dot 3rd gen under FireOS.
//
// An Echo joined to a network but without an address is stuck in a way
// nothing else repairs. Restarting Amazon's dhcpcd_wlan0 does not help on
// its own: dhcpcd comes back, reads the same lease and asks the router for
// the same address again. On a network that address does not belong to, a
// router that ignores the request leaves the Echo without IPv4 for good: on
// a TP-Link guest network, a different subnet, there was none after 40 s,
// and restarting dhcpcd by hand did not help (2026-10-22).
//
// So when the Echo has been joined with no IPv4 address for dhcpStallAfter,
// the firmware does what a WiFi change does (dropAddress, startDHCP): stop
// DHCP, delete wlan0's leases, clear the address, start DHCP afresh. The
// cases it covers all end that way: first boot at a new site, a router
// replaced by one on another subnet, a lease the router has forgotten, an
// Echo unplugged on one network and plugged in on another.
//
// The lease's own expiry cannot be relied on to do this. dhcpcd compares
// the lease file's time with the wall clock, and an Echo boots reading 2010
// until a controller sets it, which needs an address first.
//
// Timed on the monotonic clock (time.Time from time.Now() carries one, and
// Sub uses it), so a wrong wall clock, or the controller stepping it, cannot
// shorten or stretch the wait.
const (
	dhcpStallAfter = 20 * time.Second
	// At most one recovery per this long, so a network with no DHCP server
	// at all is not hit with a restart every pass.
	dhcpKickEvery = 2 * time.Minute
	dhcpWatchPoll = 5 * time.Second
)

type dhcpWatch struct {
	noAddrSince time.Time // when "joined, no address" began; zero when not in it
	lastKick    time.Time
}

// step is one pass: true when the lease should be cleared now. `changing`
// is a dashboard WiFi change in flight, which runs DHCP itself and has its
// own gates, so the watch stands aside and starts its timer afresh after.
func (w *dhcpWatch) step(now time.Time, changing, joined, hasIP bool) bool {
	if changing || !joined || hasIP {
		w.noAddrSince = time.Time{}
		return false
	}
	if w.noAddrSince.IsZero() {
		w.noAddrSince = now
		return false
	}
	if now.Sub(w.noAddrSince) < dhcpStallAfter {
		return false
	}
	if !w.lastKick.IsZero() && now.Sub(w.lastKick) < dhcpKickEvery {
		return false
	}
	w.lastKick = now
	// The fresh request gets the full wait before it counts as stuck again.
	w.noAddrSince = now
	return true
}

func changeInFlight() bool {
	mu.Lock()
	defer mu.Unlock()
	return inFlight
}

// WatchDHCP runs the stall recovery for the life of the process. A no-op
// where the board states no DHCP service, or on emOS: Android's framework or
// emOS's init owns DHCP there.
func WatchDHCP() {
	if !ownsDHCP() {
		return
	}
	var w dhcpWatch
	for {
		time.Sleep(dhcpWatchPoll)
		// The address is read through netlink, which is free; wpa_cli is a
		// process, so it is only asked once there is no address.
		hasIP := currentIPv4() != ""
		joined := !hasIP && associated()
		if w.step(time.Now(), changeInFlight(), joined, hasIP) {
			log.Printf("[wifi] joined %q with no IPv4 address for %s — clearing the lease and asking afresh",
				CurrentSSID(), dhcpStallAfter)
			kickDHCP()
		}
	}
}

// kickDHCP clears the lease and address and starts DHCP again.
func kickDHCP() {
	dropAddress()
	startDHCP()
}

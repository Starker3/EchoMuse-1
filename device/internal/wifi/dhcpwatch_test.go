package wifi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse/internal/platform"
)

func TestDHCPWatchClearsTheLeaseOnlyAfterAStall(t *testing.T) {
	var w dhcpWatch
	t0 := time.Unix(1_000_000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

	if w.step(at(0), false, true, false) || w.step(at(15), false, true, false) {
		t.Fatal("cleared the lease before the stall had lasted dhcpStallAfter")
	}
	if !w.step(at(20), false, true, false) {
		t.Fatal("joined with no address for 20s should clear the lease")
	}
	// The fresh request gets its own wait, and kicks are spaced.
	if w.step(at(45), false, true, false) {
		t.Fatal("kicked again inside dhcpKickEvery")
	}
	if !w.step(at(20+120), false, true, false) {
		t.Fatal("a network still giving no address should be retried after dhcpKickEvery")
	}
}

func TestDHCPWatchResetsWheneverTheStallBreaks(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	for name, breakIt := range map[string][3]bool{
		"got an address":      {false, true, true},
		"not joined":          {false, false, false},
		"a WiFi change began": {true, true, false},
	} {
		var w dhcpWatch
		w.step(at(0), false, true, false)
		w.step(at(15), breakIt[0], breakIt[1], breakIt[2])
		if w.step(at(25), false, true, false) {
			t.Errorf("%s: the timer did not restart, so a stall of 10s cleared the lease", name)
		}
		if !w.step(at(45), false, true, false) {
			t.Errorf("%s: a full 20s stall after the break should still clear it", name)
		}
	}
}

func TestDHCPWatchNeverActsWhileUnjoined(t *testing.T) {
	// Deleting a lease cannot fix a wrong password or a network out of range.
	var w dhcpWatch
	t0 := time.Unix(1_000_000, 0)
	for s := 0; s <= 600; s += 5 {
		if w.step(t0.Add(time.Duration(s)*time.Second), false, false, false) {
			t.Fatalf("cleared the lease at %ds while not joined to any network", s)
		}
	}
}

func TestKickDHCPClearsThenStarts(t *testing.T) {
	calls, dhcp := onDHCPBoard(t, platform.FireOS, nil)
	if err := os.WriteFile(filepath.Join(dhcp, "dhcpcd-wlan0.lease"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	kickDHCP()
	want := "stop dhcpcd_wlan0|ifconfig wlan0 0.0.0.0|start dhcpcd_wlan0"
	if got := strings.Join(*calls, "|"); got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dhcp, "dhcpcd-wlan0.lease")); err == nil {
		t.Fatal("the stale lease survived the kick")
	}
}

func TestWatchDHCPIsTheFireOSDot3sAlone(t *testing.T) {
	// A board that states no DHCP service on FireOS, and the Dot 3 on emOS:
	// the framework or emOS's init runs DHCP there, and the watch must
	// return at once.
	for _, c := range []struct {
		name    string
		base    string
		service string
	}{
		{"no service stated, FireOS", platform.FireOS, ""},
		{"Dot 3 on emOS", platform.EmOS, "dhcpcd_wlan0"},
	} {
		calls, _ := onDHCPBoard(t, c.base, nil)
		dhcpServiceOf = func() string { return c.service }
		done := make(chan struct{})
		go func() { WatchDHCP(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s: WatchDHCP kept running", c.name)
		}
		if len(*calls) != 0 {
			t.Fatalf("%s: ran commands: %q", c.name, *calls)
		}
	}
}

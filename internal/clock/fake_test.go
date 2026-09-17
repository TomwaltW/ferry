package clock

import (
	"testing"
	"time"
)

var epoch = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

func TestFakeAfterFiresAtItsDeadline(t *testing.T) {
	f := NewFake(epoch)
	ch := f.After(time.Minute)

	f.Advance(59 * time.Second)
	select {
	case at := <-ch:
		t.Fatalf("timer fired early, at %v", at)
	default:
	}

	f.Advance(time.Second)
	select {
	case at := <-ch:
		if want := epoch.Add(time.Minute); !at.Equal(want) {
			t.Errorf("fired at %v, want %v", at, want)
		}
	default:
		t.Fatal("timer did not fire once the clock passed its deadline")
	}

	if want := epoch.Add(time.Minute); !f.Now().Equal(want) {
		t.Errorf("Now = %v, want %v", f.Now(), want)
	}
}

func TestFakeAfterNonPositiveFiresImmediately(t *testing.T) {
	f := NewFake(epoch)
	select {
	case at := <-f.After(0):
		if !at.Equal(epoch) {
			t.Errorf("fired at %v, want %v", at, epoch)
		}
	default:
		t.Fatal("After(0) did not fire")
	}
}

func TestFakeBlockUntilWaitsForRegistration(t *testing.T) {
	f := NewFake(epoch)
	fired := make(chan time.Time, 1)
	go func() { fired <- <-f.After(time.Hour) }()

	f.BlockUntil(1) // returns only once the goroutine has registered its timer
	f.Advance(time.Hour)

	select {
	case at := <-fired:
		if want := epoch.Add(time.Hour); !at.Equal(want) {
			t.Errorf("fired at %v, want %v", at, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timer registered before Advance never fired")
	}
}

func TestFakeTickerRepeatsAndStops(t *testing.T) {
	f := NewFake(epoch)
	tk := f.NewTicker(time.Second)

	for i := 1; i <= 2; i++ {
		f.Advance(time.Second)
		select {
		case at := <-tk.C():
			if want := epoch.Add(time.Duration(i) * time.Second); !at.Equal(want) {
				t.Errorf("tick %d at %v, want %v", i, at, want)
			}
		default:
			t.Fatalf("tick %d never arrived", i)
		}
	}

	tk.Stop()
	f.Advance(time.Second)
	select {
	case at := <-tk.C():
		t.Fatalf("stopped ticker fired at %v", at)
	default:
	}
}

func TestFakeSetBackwardsFiresNothing(t *testing.T) {
	f := NewFake(epoch)
	ch := f.After(time.Hour)

	f.Set(epoch.Add(-time.Hour))
	select {
	case at := <-ch:
		t.Fatalf("timer fired while the clock moved backwards, at %v", at)
	default:
	}
	if want := epoch.Add(-time.Hour); !f.Now().Equal(want) {
		t.Errorf("Now = %v, want %v", f.Now(), want)
	}
}

func TestFakeFiresEveryDueTimerAtItsOwnInstant(t *testing.T) {
	f := NewFake(epoch)
	late := f.After(2 * time.Hour)
	soon := f.After(time.Hour)

	f.Advance(3 * time.Hour)

	for _, tc := range []struct {
		name string
		ch   <-chan time.Time
		want time.Time
	}{
		{"soon", soon, epoch.Add(time.Hour)},
		{"late", late, epoch.Add(2 * time.Hour)},
	} {
		select {
		case at := <-tc.ch:
			if !at.Equal(tc.want) {
				t.Errorf("%s fired carrying %v, want its own deadline %v", tc.name, at, tc.want)
			}
		default:
			t.Errorf("%s never fired", tc.name)
		}
	}

	if want := epoch.Add(3 * time.Hour); !f.Now().Equal(want) {
		t.Errorf("Now = %v, want %v", f.Now(), want)
	}
}

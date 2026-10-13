package notify

import "testing"

func TestBusCapsStreamsPerUser(t *testing.T) {
	b := NewBus()
	var chans []<-chan BusMessage
	var unsubs []func()
	for i := 0; i < maxStreamsPerUser+2; i++ {
		ch, un := b.Subscribe(1)
		chans = append(chans, ch)
		unsubs = append(unsubs, un)
	}
	if n := len(b.subs[1]); n != maxStreamsPerUser {
		t.Fatalf("streams = %d, want %d", n, maxStreamsPerUser)
	}
	if _, open := <-chans[0]; open {
		t.Fatalf("oldest stream should have been closed")
	}
	for _, un := range unsubs {
		un()
	}
	if len(b.subs) != 0 {
		t.Fatalf("subscriptions leaked")
	}
}

package notify

import "sync"

type BusMessage struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Link        string `json:"link,omitempty"`
	Event       string `json:"event,omitempty"`
	UnreadCount int    `json:"unread_count"`
}

const maxStreamsPerUser = 6

// Bus fans a freshly stored notification out to every open tab of that user.
type Bus struct {
	mu   sync.Mutex
	subs map[int64][]chan BusMessage
}

func NewBus() *Bus { return &Bus{subs: map[int64][]chan BusMessage{}} }

func (b *Bus) Subscribe(userID int64) (<-chan BusMessage, func()) {
	ch := make(chan BusMessage, 8)
	b.mu.Lock()
	list := b.subs[userID]
	for len(list) >= maxStreamsPerUser {
		close(list[0])
		list = list[1:]
	}
	b.subs[userID] = append(list, ch)
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		list := b.subs[userID]
		for i, c := range list {
			if c == ch {
				b.subs[userID] = append(list[:i], list[i+1:]...)
				close(ch)
				break
			}
		}
		if len(b.subs[userID]) == 0 {
			delete(b.subs, userID)
		}
	}
}

// Publish never blocks: a subscriber whose buffer is full has a stuck
// connection, and the bell and notifications page stay the source of truth.
func (b *Bus) Publish(userID int64, msg BusMessage) {
	b.mu.Lock()
	list := append([]chan BusMessage(nil), b.subs[userID]...)
	b.mu.Unlock()
	for _, ch := range list {
		select {
		case ch <- msg:
		default:
		}
	}
}

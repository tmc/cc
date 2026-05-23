package web

import "testing"

func TestSSEBrokerDisconnectsSlowClient(t *testing.T) {
	b := NewSSEBroker()
	ch := b.Subscribe()

	for i := 0; i < cap(ch); i++ {
		ch <- Event{Type: "queued", Data: i}
	}

	b.Publish(Event{Type: "overflow"})

	if _, ok := <-ch; !ok {
		return
	}
	for range ch {
	}
}

func TestSSEBrokerUnsubscribeIsIdempotent(t *testing.T) {
	b := NewSSEBroker()
	ch := b.Subscribe()
	b.Unsubscribe(ch)
	b.Unsubscribe(ch)
}

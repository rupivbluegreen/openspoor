package bus

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPublishDeliversToTopicAndWildcard(t *testing.T) {
	b, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tenant := uuid.New()
	var mu sync.Mutex
	var got []string
	b.Subscribe("epcis.captured", func(_ context.Context, e Event) {
		mu.Lock()
		defer mu.Unlock()
		var d map[string]int
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Error(err)
		}
		if e.TenantID != tenant || e.Key != "k1" || e.Topic != "epcis.captured" || d["n"] != 3 {
			t.Errorf("event: %+v", e)
		}
		got = append(got, "topic")
	})
	b.Subscribe("*", func(_ context.Context, e Event) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, "wild:"+e.Topic)
	})
	b.Subscribe("other", func(context.Context, Event) { t.Error("other topic must not receive") })

	b.Publish(context.Background(), "epcis.captured", tenant, "k1", map[string]int{"n": 3})
	b.Publish(context.Background(), "recall.opened", tenant, "k2", nil)

	want := []string{"topic", "wild:epcis.captured", "wild:recall.opened"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestPublishSurvivesPanicAndBadPayload(t *testing.T) {
	b, _ := New(nil)
	calls := 0
	b.Subscribe("t", func(context.Context, Event) { panic("boom") })
	b.Subscribe("t", func(context.Context, Event) { calls++ })
	b.Publish(context.Background(), "t", uuid.Nil, "", 1)
	if calls != 1 {
		t.Fatalf("subscriber after panicking one ran %d times", calls)
	}
	// Unmarshalable data is dropped before any subscriber sees it.
	b.Publish(context.Background(), "t", uuid.Nil, "", math.NaN())
	if calls != 1 {
		t.Fatalf("bad payload reached subscribers: %d", calls)
	}
}

func TestProduceSyncWithoutBroker(t *testing.T) {
	b, _ := New(nil)
	ok, err := b.ProduceSync(context.Background(), "x", [][]byte{[]byte("v")})
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestKafkaMirrorFailsWithoutReachableBroker(t *testing.T) {
	// A client is created lazily; producing to an unreachable broker must fail
	// within the context deadline rather than hang.
	b, err := New([]string{"127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.kafka == nil {
		t.Fatal("kafka client not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	b.Publish(ctx, "t", uuid.New(), "k", map[string]string{"a": "b"})
	ok, err := b.ProduceSync(ctx, "t", [][]byte{[]byte("v")})
	if !ok || err == nil {
		t.Fatalf("ok=%v err=%v, want delivery error", ok, err)
	}
}

func TestNewRejectsBadBroker(t *testing.T) {
	if b, err := New([]string{"host:notaport"}); err == nil {
		b.Close()
		t.Fatal("malformed seed broker accepted")
	}
}

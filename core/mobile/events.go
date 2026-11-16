package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AccountEvent is one entry in the account event journal: an automatic
// switch, a total failure, a failed manual switch, a recovery. The
// journal is how the user learns what the automation did and why —
// a silent rotation would leave them guessing whose quota the tunnel
// is burning. Persisted to events.json in the data dir (last 50).
type AccountEvent struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // switch | down | fail | recovered
	Text string    `json:"text"`
}

const maxAccountEvents = 50

var (
	eventsMu   sync.Mutex
	eventsList []AccountEvent
	eventsPath string
)

func loadEvents(dataDir string) {
	eventsPath = filepath.Join(dataDir, "events.json")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return
	}
	eventsMu.Lock()
	_ = json.Unmarshal(data, &eventsList)
	eventsMu.Unlock()
}

func recordEvent(kind, text string) {
	ev := AccountEvent{At: time.Now(), Kind: kind, Text: text}
	eventsMu.Lock()
	eventsList = append(eventsList, ev)
	if len(eventsList) > maxAccountEvents {
		eventsList = eventsList[len(eventsList)-maxAccountEvents:]
	}
	path := eventsPath
	data, _ := json.Marshal(eventsList)
	eventsMu.Unlock()
	if path != "" {
		_ = os.WriteFile(path, data, 0600)
	}
}

func eventsSnapshot() []AccountEvent {
	eventsMu.Lock()
	defer eventsMu.Unlock()
	out := make([]AccountEvent, len(eventsList))
	copy(out, eventsList)
	return out
}

func lastEvent() *AccountEvent {
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(eventsList) == 0 {
		return nil
	}
	ev := eventsList[len(eventsList)-1]
	return &ev
}

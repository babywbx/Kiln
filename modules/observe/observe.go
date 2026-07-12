package observe

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type Service struct {
	startedAt time.Time
	bytesIn   atomic.Uint64
	bytesOut  atomic.Uint64
	requests  atomic.Uint64
	errors    atomic.Uint64
	mu        sync.RWMutex
	sessions  map[string]SessionStat
}

type SessionStat struct {
	ChannelID string `json:"channel_id"`
	Mode      string `json:"mode"`
	// Engine is the resolved engine (native_rewrite, ffmpeg_copy, ...).
	// PackMode stays the engine's internal mode and keeps its existing ffmpeg
	// values (remote_live, local_filtered), so existing consumers do not
	// silently start seeing a different value domain.
	Engine         string    `json:"engine,omitempty"`
	PackMode       string    `json:"pack_mode,omitempty"`
	FallbackReason string    `json:"fallback_reason,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	LastTouch      time.Time `json:"last_touch"`
	State          string    `json:"state"`
	Errors         int       `json:"errors"`
	LastError      string    `json:"last_error,omitempty"`
}

type Snapshot struct {
	UptimeSec    uint64        `json:"uptime_sec"`
	BytesIn      uint64        `json:"bytes_in"`
	BytesOut     uint64        `json:"bytes_out"`
	Requests     uint64        `json:"requests"`
	Errors       uint64        `json:"errors"`
	Goroutines   int           `json:"goroutines"`
	Sessions     []SessionStat `json:"sessions"`
	SessionCount int           `json:"session_count"`
}

func New() *Service {
	return &Service{
		startedAt: time.Now(),
		sessions:  map[string]SessionStat{},
	}
}

func (s *Service) AddBytesIn(n int64) {
	if n > 0 {
		s.bytesIn.Add(uint64(n))
	}
}

func (s *Service) AddBytesOut(n int64) {
	if n > 0 {
		s.bytesOut.Add(uint64(n))
	}
}

func (s *Service) IncRequest() { s.requests.Add(1) }
func (s *Service) IncError()   { s.errors.Add(1) }

func (s *Service) UpsertSession(st SessionStat) {
	s.mu.Lock()
	s.sessions[st.ChannelID] = st
	s.mu.Unlock()
}

func (s *Service) RemoveSession(channelID string) {
	s.mu.Lock()
	delete(s.sessions, channelID)
	s.mu.Unlock()
}

func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	list := make([]SessionStat, 0, len(s.sessions))
	for _, v := range s.sessions {
		list = append(list, v)
	}
	n := len(s.sessions)
	s.mu.RUnlock()
	return Snapshot{
		UptimeSec:    uint64(time.Since(s.startedAt).Seconds()),
		BytesIn:      s.bytesIn.Load(),
		BytesOut:     s.bytesOut.Load(),
		Requests:     s.requests.Load(),
		Errors:       s.errors.Load(),
		Goroutines:   runtime.NumGoroutine(),
		Sessions:     list,
		SessionCount: n,
	}
}

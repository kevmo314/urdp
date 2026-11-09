package server

import (
	"context"
	"encoding/json"
	"log"

	"github.com/kevmo314/urdp/internal/capture"
)

const maxCursorCache = 256

// The cursor lock orders initial snapshots and broadcasts so an opening viewer
// cannot receive a stale snapshot after a newer notification.
func (s *Server) addCursorSession(sess *session) {
	s.cursorMu.Lock()
	if sess.cursorClosed {
		s.cursorMu.Unlock()
		return
	}
	if s.cursorSessions == nil {
		s.cursorSessions = make(map[*session]bool)
	}
	s.cursorSessions[sess] = true
	if s.cursorCurrent != nil {
		sess.sendCursor(*s.cursorCurrent)
	}
	// Watch only while someone is viewing: the Windows and macOS watchers
	// poll. The last viewer leaving stops it; the next one restarts it.
	if s.cursorStop == nil {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		s.cursorStop, s.cursorDone = cancel, done
		prev := s.cursorPrevDone
		go func() {
			defer close(done)
			if prev != nil {
				<-prev // one watcher at a time
			}
			if err := s.cfg.WatchCursor(ctx, func(c capture.Cursor) {
				if ctx.Err() == nil {
					s.broadcastCursor(c)
				}
			}); err != nil && ctx.Err() == nil {
				log.Printf("cursor: %v", err)
			}
		}()
	}
	s.cursorMu.Unlock()
}

func (s *Server) removeCursorSession(sess *session) {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	sess.cursorClosed = true
	delete(s.cursorSessions, sess)
	if len(s.cursorSessions) == 0 && s.cursorStop != nil {
		s.cursorStop()
		s.cursorStop, s.cursorPrevDone = nil, s.cursorDone
		// The next watcher reports the shape afresh.
		s.cursorCurrent = nil
	}
}

func (s *Server) broadcastCursor(c capture.Cursor) {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	c.PNG = append([]byte(nil), c.PNG...)
	s.cursorCurrent = &c
	for sess := range s.cursorSessions {
		sess.sendCursor(c)
	}
}

func (s *session) sendCursor(c capture.Cursor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := struct {
		Type   string  `json:"t"`
		ID     *uint64 `json:"id,omitempty"`
		CSS    string  `json:"css,omitempty"`
		PNG    []byte  `json:"png,omitempty"`
		X      *int    `json:"x,omitempty"`
		Y      *int    `json:"y,omitempty"`
		Hidden bool    `json:"hidden,omitempty"`
	}{Type: "cursor", Hidden: c.Hidden}
	if !c.Hidden {
		msg.ID = &c.Serial
		if !s.cursorIDs[c.Serial] {
			msg.CSS, msg.PNG = c.CSS, c.PNG
			if len(c.PNG) != 0 {
				msg.X, msg.Y = &c.HotX, &c.HotY
			}
		}
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	if err := s.sendLocked(string(data)); err == nil && !c.Hidden {
		// Apps can mint a new cursor handle per change; bound what this
		// viewer is assumed to have cached (it re-sends after a reset).
		if s.cursorIDs == nil || len(s.cursorIDs) >= maxCursorCache {
			s.cursorIDs = make(map[uint64]bool)
		}
		s.cursorIDs[c.Serial] = true
	}
}

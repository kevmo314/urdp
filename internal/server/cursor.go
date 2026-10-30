package server

import (
	"context"
	"encoding/json"
	"log"

	"github.com/kevmo314/urdp/internal/capture"
)

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
	s.cursorMu.Unlock()
	s.cursorOnce.Do(func() {
		go func() {
			if err := s.cfg.WatchCursor(context.Background(), s.broadcastCursor); err != nil {
				log.Printf("cursor: %v", err)
			}
		}()
	})
}

func (s *Server) removeCursorSession(sess *session) {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	sess.cursorClosed = true
	delete(s.cursorSessions, sess)
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
		if s.cursorIDs == nil {
			s.cursorIDs = make(map[uint64]bool)
		}
		s.cursorIDs[c.Serial] = true
	}
}

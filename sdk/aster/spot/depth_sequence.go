package spot

import (
	"fmt"
	"sync"
)

type DepthSequence struct {
	mu           sync.Mutex
	lastUpdateID int64
}

func NewDepthSequence(lastUpdateID int64) *DepthSequence {
	return &DepthSequence{lastUpdateID: lastUpdateID}
}

func (s *DepthSequence) LastUpdateID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUpdateID
}

func (s *DepthSequence) Accept(event WsDepthEvent) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.FirstUpdateID <= 0 || event.FinalUpdateID < event.FirstUpdateID {
		return false, &DepthSequenceGapError{
			expected:      s.lastUpdateID + 1,
			firstUpdateID: event.FirstUpdateID,
			finalUpdateID: event.FinalUpdateID,
		}
	}
	if event.FinalUpdateID <= s.lastUpdateID {
		return false, nil
	}

	expected := s.lastUpdateID + 1
	if event.FirstUpdateID > expected || event.FinalUpdateID < expected ||
		(event.FinalUpdateIDLast != 0 && event.FinalUpdateIDLast != s.lastUpdateID) {
		return false, &DepthSequenceGapError{
			expected:      expected,
			firstUpdateID: event.FirstUpdateID,
			finalUpdateID: event.FinalUpdateID,
		}
	}
	s.lastUpdateID = event.FinalUpdateID
	return true, nil
}

type DepthSequenceGapError struct {
	expected      int64
	firstUpdateID int64
	finalUpdateID int64
}

func (e *DepthSequenceGapError) Error() string {
	return fmt.Sprintf("aster spot depth sequence gap: expected update %d, received [%d,%d]", e.expected, e.firstUpdateID, e.finalUpdateID)
}

func (e *DepthSequenceGapError) Expected() int64 { return e.expected }

func (e *DepthSequenceGapError) FirstUpdateID() int64 { return e.firstUpdateID }

func (e *DepthSequenceGapError) FinalUpdateID() int64 { return e.finalUpdateID }

package cmaf

import (
	"bytes"

	"github.com/Eyevinn/mp4ff/mp4"
)

type EventMessage struct {
	Version               uint8
	Timescale             uint32
	PresentationTimeDelta uint32
	PresentationTime      uint64
	EventDuration         uint32
	ID                    uint32
	SchemeIDURI           string
	Value                 string
	MessageData           []byte
}

func eventMessages(segment *mp4.MediaSegment) []EventMessage {
	var events []EventMessage
	for _, fragment := range segment.Fragments {
		for _, event := range fragment.Emsgs {
			if event == nil {
				continue
			}
			events = append(events, eventMessage(event))
		}
	}
	return events
}

func eventMessage(event *mp4.EmsgBox) EventMessage {
	return EventMessage{
		Version:               event.Version,
		Timescale:             event.TimeScale,
		PresentationTimeDelta: event.PresentationTimeDelta,
		PresentationTime:      event.PresentationTime,
		EventDuration:         event.EventDuration,
		ID:                    event.ID,
		SchemeIDURI:           event.SchemeIDURI,
		Value:                 event.Value,
		MessageData:           bytes.Clone(event.MessageData),
	}
}

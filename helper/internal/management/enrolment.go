package management

import "github.com/chrisbanes/portico/helper/internal/service"

// enqueueEvent is this session's private enrolment sink. The engine routes
// events to the requesting session only and calls this under its lock, so
// it only queues. The URL is never logged or stored here.
func (c *conn) enqueueEvent(event service.EnrolmentEvent) {
	select {
	case c.events <- event:
	default:
		c.overflow()
	}
}

func enrolmentEventFrame(e service.EnrolmentEvent) enrolmentFrame {
	return enrolmentFrame{Type: "enrolment", Event: e.Kind, RequestID: e.RequestID, PortalID: e.PortalID, Nonce: e.Nonce, URL: e.URL}
}

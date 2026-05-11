package ward

import "strconv"

// Event represents an OSC 133 semantic prompt marker.
type Event int

const (
	EventPromptStart     Event = iota // ESC ] 133 ; A ST
	EventCommandStart                 // ESC ] 133 ; B ST
	EventCommandExecuted              // ESC ] 133 ; C ST
	EventCommandFinished              // ESC ] 133 ; D [; exit_code] ST
)

// Zone represents the current shell lifecycle phase.
type Zone int

const (
	ZoneUnknown Zone = iota // No marker seen, or after D
	ZonePrompt              // Between A and B
	ZoneInput               // Between B and C
	ZoneOutput              // Between C and D
)

const (
	esc          byte = 0x1B
	bel          byte = 0x07
	backslash    byte = '\\'
	rightBracket byte = ']'
	paramBufCap       = 32
)

type parserState int

const (
	stateGround parserState = iota
	stateEsc
	stateOscParam
	stateOscEsc
)

// OSC133Parser is a streaming, zero-allocation parser for OSC 133 escape sequences.
type OSC133Parser struct {
	state    parserState
	zone     Zone
	paramBuf [paramBufCap]byte
	paramLen int
}

// NewOSC133Parser creates a parser in the initial ground/unknown state.
func NewOSC133Parser() *OSC133Parser {
	return &OSC133Parser{
		state: stateGround,
		zone:  ZoneUnknown,
	}
}

// Zone returns the current semantic zone.
func (p *OSC133Parser) Zone() Zone {
	return p.zone
}

// Push processes a chunk of bytes, calling onEvent for every OSC 133 marker found.
// The caller is responsible for forwarding bytes to their destination.
func (p *OSC133Parser) Push(data []byte, onEvent func(Event, *int)) {
	for _, b := range data {
		switch p.state {
		case stateGround:
			if b == esc {
				p.state = stateEsc
			}
		case stateEsc:
			if b == rightBracket {
				p.state = stateOscParam
				p.paramLen = 0
			} else {
				p.state = stateGround
			}
		case stateOscParam:
			if b == bel {
				p.dispatch(onEvent)
				p.state = stateGround
			} else if b == esc {
				p.state = stateOscEsc
			} else if p.paramLen < paramBufCap {
				p.paramBuf[p.paramLen] = b
				p.paramLen++
			}
		case stateOscEsc:
			if b == backslash {
				p.dispatch(onEvent)
			}
			p.state = stateGround
		}
	}
}

func (p *OSC133Parser) dispatch(onEvent func(Event, *int)) {
	params := p.paramBuf[:p.paramLen]

	if len(params) < 5 || string(params[:4]) != "133;" {
		return
	}

	cmd := params[4]
	switch cmd {
	case 'A':
		p.zone = ZonePrompt
		onEvent(EventPromptStart, nil)
	case 'B':
		p.zone = ZoneInput
		onEvent(EventCommandStart, nil)
	case 'C':
		p.zone = ZoneOutput
		onEvent(EventCommandExecuted, nil)
	case 'D':
		var exitCode *int
		if len(params) > 6 && params[5] == ';' {
			if code, err := strconv.ParseInt(string(params[6:]), 10, 32); err == nil {
				v := int(code)
				exitCode = &v
			}
		}
		p.zone = ZoneUnknown
		onEvent(EventCommandFinished, exitCode)
	}
}

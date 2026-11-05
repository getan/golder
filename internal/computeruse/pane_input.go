package computeruse

// paneInputFilter removes terminal protocol replies before they can be
// interpreted as operator commands. An overlong/incomplete reply fails closed
// by asking the pane to quit; Ctrl+G and Ctrl+C interrupt even inside a reply.
type paneInputFilter struct {
	state byte
	size  int
}

func (p *paneInputFilter) accept(r rune) (rune, bool) {
	if p.state != 0 && (r == 0x07 || r == 0x03) {
		p.state, p.size = 0, 0
		return r, true
	}
	if p.state != 0 {
		p.size++
		if p.size > 4096 {
			p.state, p.size = 0, 0
			return 0x03, true
		}
	}
	switch p.state {
	case 0:
		if r == 0x1b {
			p.state, p.size = 1, 0
			return 0, false
		}
		return r, true
	case 1:
		switch r {
		case '[', 'O':
			p.state = 2 // CSI / SS3
		case ']', '_', 'P', '^', 'X':
			p.state = 3 // OSC / APC / DCS / PM / SOS
		default:
			p.state, p.size = 0, 0
		}
	case 2:
		if r >= 0x40 && r <= 0x7e {
			p.state, p.size = 0, 0
		}
	case 3:
		if r == 0x1b {
			p.state = 4
		}
	case 4:
		if r == '\\' {
			p.state, p.size = 0, 0
		} else if r != 0x1b {
			p.state = 3
		}
	}
	return 0, false
}

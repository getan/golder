package docxmcp

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
)

// xmlElem is an element of a document part with the byte range of its tags.
// Mutations splice these ranges, so every byte outside an edited element is
// preserved: namespaces, rsid attributes, styles and unrelated markup.
type xmlElem struct {
	name     string
	start    int // '<' of the start tag
	startEnd int // just after the start tag's '>'
	endStart int // '<' of the end tag, or startEnd for a self-closing tag
	end      int // just after the end tag's '>'
	self     bool
	parent   *xmlElem
	children []*xmlElem
}

func parseXMLTree(data []byte) (*xmlElem, error) {
	root := &xmlElem{name: "#root", endStart: len(data), end: len(data)}
	stack := []*xmlElem{root}
	for i := 0; i < len(data); {
		lt := bytes.IndexByte(data[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		switch {
		case bytes.HasPrefix(data[i:], []byte("<!--")):
			end := bytes.Index(data[i+4:], []byte("-->"))
			if end < 0 {
				return nil, errf(codeReadFailed, "malformed document XML")
			}
			i += 4 + end + 3
			continue
		case bytes.HasPrefix(data[i:], []byte("<![CDATA[")):
			end := bytes.Index(data[i+9:], []byte("]]>"))
			if end < 0 {
				return nil, errf(codeReadFailed, "malformed document XML")
			}
			i += 9 + end + 3
			continue
		case bytes.HasPrefix(data[i:], []byte("<?")):
			end := bytes.Index(data[i+2:], []byte("?>"))
			if end < 0 {
				return nil, errf(codeReadFailed, "malformed document XML")
			}
			i += 2 + end + 2
			continue
		case bytes.HasPrefix(data[i:], []byte("<!")):
			end := bytes.IndexByte(data[i:], '>')
			if end < 0 {
				return nil, errf(codeReadFailed, "malformed document XML")
			}
			i += end + 1
			continue
		}
		if bytes.HasPrefix(data[i:], []byte("</")) {
			gt, err := xmlTagEnd(data, i+2)
			if err != nil {
				return nil, err
			}
			name := xmlLocalName(xmlTagName(data[i+2 : gt]))
			if len(stack) <= 1 || stack[len(stack)-1].name != name {
				return nil, errf(codeReadFailed, "malformed document XML")
			}
			top := stack[len(stack)-1]
			top.endStart, top.end = i, gt+1
			stack = stack[:len(stack)-1]
			i = gt + 1
			continue
		}
		gt, err := xmlTagEnd(data, i+1)
		if err != nil {
			return nil, err
		}
		tag := bytes.TrimSpace(data[i+1 : gt])
		self := len(tag) > 0 && tag[len(tag)-1] == '/'
		name := xmlLocalName(xmlTagName(tag))
		n := &xmlElem{name: name, start: i, startEnd: gt + 1, self: self}
		if self {
			n.endStart, n.end = gt+1, gt+1
		}
		parent := stack[len(stack)-1]
		n.parent = parent
		parent.children = append(parent.children, n)
		if !self {
			stack = append(stack, n)
		}
		i = gt + 1
	}
	if len(stack) != 1 {
		return nil, errf(codeReadFailed, "malformed document XML")
	}
	return root, nil
}

func xmlTagEnd(data []byte, from int) (int, error) {
	quote := byte(0)
	for i := from; i < len(data); i++ {
		c := data[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '>' {
			return i, nil
		}
	}
	return 0, errf(codeReadFailed, "malformed document XML")
}

func xmlTagName(tag []byte) string {
	i := 0
	for i < len(tag) && tag[i] != ' ' && tag[i] != '\t' && tag[i] != '\r' && tag[i] != '\n' && tag[i] != '/' && tag[i] != '>' {
		i++
	}
	return string(tag[:i])
}

func xmlLocalName(name string) string {
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func (n *xmlElem) direct(name string) []*xmlElem {
	var out []*xmlElem
	for _, c := range n.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func (n *xmlElem) firstDirect(name string) *xmlElem {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (n *xmlElem) descendants(name string) []*xmlElem {
	var out []*xmlElem
	var walk func(*xmlElem)
	walk = func(x *xmlElem) {
		if x.name == name {
			out = append(out, x)
		}
		for _, c := range x.children {
			walk(c)
		}
	}
	walk(n)
	return out
}

func (n *xmlElem) firstDescendant(name string) *xmlElem {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
		if found := c.firstDescendant(name); found != nil {
			return found
		}
	}
	return nil
}

// xmlAttr returns the unescaped value of the attribute whose local name is
// local, from any namespace prefix.
func xmlAttr(data []byte, n *xmlElem, local string) string {
	if n == nil || n.startEnd <= n.start || n.startEnd > len(data) {
		return ""
	}
	b := data[n.start:n.startEnd]
	i := 1
	for i < len(b) && !xmlSpace(b[i]) && b[i] != '>' && b[i] != '/' {
		i++
	}
	for i < len(b) {
		for i < len(b) && xmlSpace(b[i]) {
			i++
		}
		if i >= len(b) || b[i] == '>' || b[i] == '/' {
			break
		}
		nameStart := i
		for i < len(b) && !xmlSpace(b[i]) && b[i] != '=' && b[i] != '>' && b[i] != '/' {
			i++
		}
		name := string(b[nameStart:i])
		for i < len(b) && xmlSpace(b[i]) {
			i++
		}
		if i >= len(b) || b[i] != '=' {
			continue
		}
		i++
		for i < len(b) && xmlSpace(b[i]) {
			i++
		}
		if i >= len(b) || (b[i] != '"' && b[i] != '\'') {
			break
		}
		quote := b[i]
		i++
		valueStart := i
		for i < len(b) && b[i] != quote {
			i++
		}
		value := string(b[valueStart:i])
		if i < len(b) {
			i++
		}
		if xmlLocalName(name) == local {
			return xmlUnescape(value)
		}
	}
	return ""
}

func xmlSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func xmlUnescape(s string) string {
	if !strings.ContainsAny(s, "&") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], ';')
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		entity := s[i+1 : i+end]
		switch entity {
		case "amp":
			b.WriteByte('&')
		case "lt":
			b.WriteByte('<')
		case "gt":
			b.WriteByte('>')
		case "quot":
			b.WriteByte('"')
		case "apos":
			b.WriteByte('\'')
		default:
			if strings.HasPrefix(entity, "#x") {
				if v, err := strconv.ParseInt(entity[2:], 16, 32); err == nil {
					b.WriteRune(rune(v))
					i += end + 1
					continue
				}
			} else if strings.HasPrefix(entity, "#") {
				if v, err := strconv.ParseInt(entity[1:], 10, 32); err == nil {
					b.WriteRune(rune(v))
					i += end + 1
					continue
				}
			}
			b.WriteString(s[i : i+end+1])
		}
		i += end + 1
	}
	return b.String()
}

func xmlEscapeText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

package webtool

import (
	"bytes"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Markdown turns an HTML page into Markdown a model can read: headings,
// paragraphs, lists, links, emphasis, code and preformatted text, with
// scripts, styles and the page's head left out. It is a reader's
// conversion, not a faithful one: what a page does with its layout is lost,
// and what it says stays.
func Markdown(page string) string {
	c := &converter{}
	c.run(page)
	return c.finish()
}

// markdownMax is the most Markdown one page is turned into: many times
// what a model is given of a page, so that nothing it would read is lost,
// and a bound on what a page made to blow up can cost (a fresh-eyes review
// found a page of nested lists that grew to gigabytes). maxLists and
// maxLinks bound the open lists and links the converter keeps track of.
const (
	markdownMax = 4 << 20
	maxLists    = 16
	maxLinks    = 64
)

// skipped are the elements whose content is not text for a reader.
var skipped = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true,
	"head": true, "iframe": true, "object": true, "canvas": true, "select": true, "button": true}

// blocks are the elements that stand apart from the text around them.
var blocks = map[string]bool{"p": true, "div": true, "section": true, "article": true, "header": true, "footer": true,
	"main": true, "nav": true, "aside": true, "blockquote": true, "ul": true, "ol": true, "table": true, "tr": true,
	"form": true, "figure": true, "figcaption": true, "dl": true, "dt": true, "dd": true, "address": true,
	"fieldset": true, "details": true, "summary": true, "hgroup": true, "body": true, "html": true, "center": true}

var spaces = regexp.MustCompile(`[ \t\r\n\f]+`)

type converter struct {
	// out is what was written so far: a buffer that can be cut back in
	// place, so that ending a line or a link costs what it writes and not
	// the length of the page.
	out bytes.Buffer
	// fresh says nothing has been written on the line yet but its marker
	// (a list item's), so blanks that would start it are dropped.
	fresh bool
	skip  int    // inside skipped elements
	pre   int    // inside <pre>
	list  []int  // the counters of the open lists: 0 for <ul>
	deep  int    // open lists past maxLists, which are not kept
	link  []link // the open links
	lost  int    // open links past maxLinks, which are not kept
	col   int    // the cell of the row the converter is in
	cut   bool   // the page was cut at markdownMax
}

type link struct {
	href  string
	start int
}

func (c *converter) run(page string) {
	for len(page) > 0 {
		if c.out.Len() > markdownMax {
			c.cut = true
			return
		}
		lt := strings.IndexByte(page, '<')
		if lt < 0 {
			c.text(page)
			return
		}
		c.text(page[:lt])
		page = page[lt:]
		switch {
		case strings.HasPrefix(page, "<!--"):
			end := strings.Index(page, "-->")
			if end < 0 {
				return
			}
			page = page[end+3:]
		case strings.HasPrefix(page, "<!") || strings.HasPrefix(page, "<?"):
			end := strings.IndexByte(page, '>')
			if end < 0 {
				return
			}
			page = page[end+1:]
		default:
			end := tagEnd(page)
			if end < 0 {
				c.text(page)
				return
			}
			c.tag(page[1:end])
			page = page[end+1:]
		}
	}
}

// tagEnd finds the > that closes the tag at the start of s, past any
// quoted attribute value.
func tagEnd(s string) int {
	var quote byte
	for i := 1; i < len(s); i++ {
		switch b := s[i]; {
		case quote != 0:
			if b == quote {
				quote = 0
			}
		case b == '"' || b == '\'':
			quote = b
		case b == '>':
			return i
		}
	}
	return -1
}

func (c *converter) text(s string) {
	if c.skip > 0 || s == "" || c.cut {
		return
	}
	s = html.UnescapeString(s)
	if room := markdownMax - c.out.Len(); len(s) > room {
		room = max(room, 0)
		for room > 0 && !utf8.RuneStart(s[room]) {
			room--
		}
		s, c.cut = s[:room], true
	}
	if c.pre > 0 {
		c.out.WriteString(s)
		return
	}
	s = spaces.ReplaceAllString(s, " ")
	if c.fresh || c.out.Len() == 0 {
		s = strings.TrimLeft(s, " ")
	}
	if s == "" {
		return
	}
	c.out.WriteString(s)
	c.fresh = false
}

// breakLine ends the line, and with blank, the paragraph.
func (c *converter) breakLine(blank bool) {
	c.out.Truncate(len(bytes.TrimRight(c.out.Bytes(), " ")))
	if c.out.Len() == 0 {
		return
	}
	want := []byte("\n")
	if blank {
		want = []byte("\n\n")
	}
	for !bytes.HasSuffix(c.out.Bytes(), want) {
		c.out.WriteByte('\n')
	}
	c.fresh = true
}

func (c *converter) tag(t string) {
	closing := strings.HasPrefix(t, "/")
	t = strings.TrimPrefix(t, "/")
	t = strings.TrimSpace(t)
	// A tag that closes itself (<svg/>) opens nothing to skip.
	selfClosed := strings.HasSuffix(t, "/")
	t = strings.TrimSuffix(t, "/")
	name, attrs, _ := strings.Cut(t, " ")
	if i := strings.IndexAny(name, "\t\n\r"); i >= 0 {
		name, attrs = name[:i], name[i:]+" "+attrs
	}
	name = strings.ToLower(name)
	if skipped[name] {
		if closing {
			if c.skip > 0 {
				c.skip--
			}
		} else if !selfClosed {
			c.skip++
		}
		return
	}
	if c.skip > 0 {
		return
	}
	switch {
	case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
		c.breakLine(true)
		if !closing {
			c.out.WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
			c.fresh = true
		}
	case name == "br":
		c.breakLine(false)
	case name == "hr":
		c.breakLine(true)
		c.out.WriteString("---")
		c.breakLine(true)
	case name == "pre":
		c.breakLine(true)
		if closing {
			if c.pre > 0 {
				c.pre--
			}
			c.out.WriteString("```")
			c.breakLine(true)
		} else {
			c.pre++
			c.out.WriteString("```\n")
		}
	case name == "code" && c.pre == 0, name == "kbd" && c.pre == 0, name == "samp" && c.pre == 0:
		c.out.WriteString("`")
	case name == "strong" || name == "b":
		c.out.WriteString("**")
	case name == "em" || name == "i":
		c.out.WriteString("*")
	case name == "ul" || name == "ol":
		c.breakLine(true)
		switch {
		case closing && c.deep > 0:
			c.deep--
		case closing:
			if len(c.list) > 0 {
				c.list = c.list[:len(c.list)-1]
			}
		case len(c.list) >= maxLists:
			c.deep++
		case name == "ol":
			c.list = append(c.list, 1)
		default:
			c.list = append(c.list, 0)
		}
	case name == "li":
		c.breakLine(false)
		if closing {
			return
		}
		indent := strings.Repeat("  ", max(len(c.list)-1, 0))
		if n := len(c.list); n > 0 && c.list[n-1] > 0 {
			c.out.WriteString(indent + strconv.Itoa(c.list[n-1]) + ". ")
			c.list[n-1]++
		} else {
			c.out.WriteString(indent + "- ")
		}
		c.fresh = true
	case name == "a":
		if closing {
			if c.lost > 0 {
				c.lost--
				return
			}
			if n := len(c.link); n > 0 {
				l := c.link[n-1]
				c.link = c.link[:n-1]
				if l.href != "" && l.start <= c.out.Len() {
					text := strings.TrimSpace(string(c.out.Bytes()[l.start:]))
					c.out.Truncate(l.start)
					if text == "" {
						text = l.href
					}
					c.out.WriteString("[" + text + "](" + l.href + ")")
				}
			}
			return
		}
		if len(c.link) >= maxLinks {
			c.lost++
			return
		}
		c.link = append(c.link, link{href: attr(attrs, "href"), start: c.out.Len()})
	case name == "img":
		if alt := strings.TrimSpace(attr(attrs, "alt")); alt != "" {
			c.out.WriteString("[image: " + alt + "]")
		}
	case name == "td" || name == "th":
		if !closing {
			if c.col > 0 {
				c.out.WriteString(" | ")
			}
			c.col++
		}
	case name == "tr":
		c.col = 0
		c.breakLine(false)
	case name == "title" && !closing:
		c.out.WriteString("# ")
		c.fresh = true
	case name == "title":
		c.breakLine(true)
	case blocks[name]:
		c.breakLine(true)
	}
}

// attrs are the patterns of the two attributes the converter reads.
var attrPatterns = map[string]*regexp.Regexp{
	"href": regexp.MustCompile(`(?i)(?:^|\s)href\s*=\s*("([^"]*)"|'([^']*)'|([^\s"'>]+))`),
	"alt":  regexp.MustCompile(`(?i)(?:^|\s)alt\s*=\s*("([^"]*)"|'([^']*)'|([^\s"'>]+))`),
}

// attr returns the value of one attribute (href or alt) in a tag's
// attributes.
func attr(attrs, name string) string {
	m := attrPatterns[name].FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	return html.UnescapeString(firstOf(m[2], m[3], m[4]))
}

func (c *converter) finish() string {
	s := c.out.String()
	if c.cut {
		s += "\n\n[The page went on past what gnosh turns into text.]"
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	s = strings.Join(lines, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s) + "\n"
}

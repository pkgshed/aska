package discovery

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// metaName is the name attribute that marks a discovery tag.
const metaName = "aska-import"

// metaTag is one parsed aska-import tag.
type metaTag struct {
	prefix       string
	vcs          string
	repoURL      string
	subdirectory string
}

// parseMetaTags reads aska-import tags from the head of an HTML page. Like
// the go command, it uses a lenient XML tokenizer, accepts only UTF-8 and
// ASCII and stops at the body, so the rest of the page is never parsed.
func parseMetaTags(page io.Reader) ([]metaTag, error) {
	decoder := xml.NewDecoder(page)
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	decoder.CharsetReader = charsetReader

	var tags []metaTag
	for {
		token, err := decoder.RawToken()
		switch {
		case errors.Is(err, io.EOF):
			return tags, nil
		case err != nil:
			if len(tags) > 0 {
				return tags, nil
			}
			return nil, err
		}

		if isEndOfHead(token) {
			return tags, nil
		}

		tag, found, err := metaTagFromToken(token)
		if err != nil {
			return nil, err
		}
		if found {
			tags = append(tags, tag)
		}
	}
}

func isEndOfHead(token xml.Token) bool {
	switch element := token.(type) {
	case xml.StartElement:
		return strings.EqualFold(element.Name.Local, "body")
	case xml.EndElement:
		return strings.EqualFold(element.Name.Local, "head")
	default:
		return false
	}
}

func metaTagFromToken(token xml.Token) (metaTag, bool, error) {
	element, isStart := token.(xml.StartElement)
	if !isStart || !strings.EqualFold(element.Name.Local, "meta") {
		return metaTag{}, false, nil
	}
	if attribute(element, "name") != metaName {
		return metaTag{}, false, nil
	}

	content := attribute(element, "content")
	fields := strings.Fields(content)
	if len(fields) != 3 && len(fields) != 4 {
		return metaTag{}, false, fmt.Errorf("malformed %s content %q", metaName, content)
	}

	tag := metaTag{prefix: fields[0], vcs: fields[1], repoURL: fields[2]}
	if len(fields) == 4 {
		tag.subdirectory = fields[3]
	}
	return tag, true, nil
}

func attribute(element xml.StartElement, name string) string {
	for _, attr := range element.Attr {
		if strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "utf-8", "ascii":
		return input, nil
	default:
		return nil, fmt.Errorf("unsupported charset %q", charset)
	}
}

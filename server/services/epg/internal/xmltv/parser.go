// Package xmltv parses XMLTV-formatted XML into structured Go types.
// XMLTV is the standard EPG (Electronic Program Guide) data format used
// by most IPTV providers. This parser handles the common subset of the
// XMLTV spec: channels with display names and icons, programmes with
// title, description, categories, and ratings.
//
// XMLTV date format: YYYYMMDDHHmmss +ZZZZ (e.g. "20260223140000 +0000")
package xmltv

import (
	"encoding/xml"
	"fmt"
	"io"
	"time"
)

// xmltvDateLayout is the XMLTV date format: YYYYMMDDHHmmss ±HHMM
const xmltvDateLayout = "20060102150405 -0700"

// XMLTVChannel represents a parsed <channel> element.
type XMLTVChannel struct {
	ID          string // XMLTV id attribute
	DisplayName string // <display-name> content
	IconSrc     string // <icon src="..."/> URL
}

// XMLTVProgramme represents a parsed <programme> element.
type XMLTVProgramme struct {
	ChannelID   string    // channel attribute (matches XMLTVChannel.ID)
	Start       time.Time // parsed start time
	Stop        time.Time // parsed end time
	Title       string    // <title> content
	Description string    // <desc> content
	Category    string    // first <category> content
	Rating      string    // <rating><value> or <rating system=""> content
	IconSrc     string    // <icon src="..."/>
}

// Result holds the parsed XMLTV document.
type Result struct {
	Channels   []XMLTVChannel
	Programmes []XMLTVProgramme
}

// xmlChannel is the raw XML structure for <channel>.
type xmlChannel struct {
	ID          string `xml:"id,attr"`
	DisplayName string `xml:"display-name"`
	Icon        struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
}

// xmlProgramme is the raw XML structure for <programme>.
type xmlProgramme struct {
	Start   string `xml:"start,attr"`
	Stop    string `xml:"stop,attr"`
	Channel string `xml:"channel,attr"`
	Title   string `xml:"title"`
	Desc    string `xml:"desc"`
	Icon    struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
	Category []string `xml:"category"`
	Rating   []struct {
		System string `xml:"system,attr"`
		Value  string `xml:"value"`
	} `xml:"rating"`
}

// parseXMLTVDate parses an XMLTV timestamp string into time.Time.
// Handles both "YYYYMMDDHHmmss +HHMM" and "YYYYMMDDHHmmss +HH:MM" variants.
func parseXMLTVDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty date string")
	}
	t, err := time.Parse(xmltvDateLayout, s)
	if err != nil {
		// Try without the colon in timezone (some sources omit space)
		t, err = time.Parse("20060102150405", s)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse xmltv date %q: %w", s, err)
		}
	}
	return t, nil
}

// ParseReader parses an XMLTV XML document from the given reader.
// Returns a Result containing all channels and programmes found.
// Malformed individual elements are skipped (with no error returned) to
// ensure a partial feed yields maximum usable data.
func ParseReader(r io.Reader) (*Result, error) {
	decoder := xml.NewDecoder(r)
	result := &Result{}

	var inTV bool
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml token: %w", err)
		}

		switch el := token.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "tv":
				inTV = true

			case "channel":
				if !inTV {
					continue
				}
				var raw xmlChannel
				if err := decoder.DecodeElement(&raw, &el); err != nil {
					continue // skip malformed channel
				}
				if raw.ID == "" {
					continue
				}
				result.Channels = append(result.Channels, XMLTVChannel{
					ID:          raw.ID,
					DisplayName: raw.DisplayName,
					IconSrc:     raw.Icon.Src,
				})

			case "programme":
				if !inTV {
					continue
				}
				var raw xmlProgramme
				if err := decoder.DecodeElement(&raw, &el); err != nil {
					continue // skip malformed programme
				}

				start, err := parseXMLTVDate(raw.Start)
				if err != nil {
					continue // skip programme with unparseable start time
				}
				stop, err := parseXMLTVDate(raw.Stop)
				if err != nil {
					continue // skip programme with unparseable stop time
				}

				category := ""
				if len(raw.Category) > 0 {
					category = raw.Category[0]
				}

				rating := ""
				for _, rr := range raw.Rating {
					if rr.Value != "" {
						rating = rr.Value
						break
					}
				}

				result.Programmes = append(result.Programmes, XMLTVProgramme{
					ChannelID:   raw.Channel,
					Start:       start,
					Stop:        stop,
					Title:       raw.Title,
					Description: raw.Desc,
					Category:    category,
					Rating:      rating,
					IconSrc:     raw.Icon.Src,
				})
			}

		case xml.EndElement:
			if el.Name.Local == "tv" {
				inTV = false
			}
		}
	}

	return result, nil
}

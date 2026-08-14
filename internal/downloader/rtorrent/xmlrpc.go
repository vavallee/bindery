package rtorrent

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// This file implements just enough of XML-RPC to talk to rTorrent: a request
// encoder for the four argument shapes rTorrent commands take (string, int,
// bool, base64 blob) and a response decoder that understands scalars and
// nested arrays.
//
// It is deliberately hand-rolled on top of encoding/xml rather than pulling in
// a third-party XML-RPC library. rTorrent's dialect is tiny — no <struct>
// arguments, no <dateTime.iso8601>, no introspection round-trips — and the two
// available Go XML-RPC packages both bring reflection-based marshalling and a
// wider surface than the handful of calls in client.go justify.

// Fault is an XML-RPC <fault> reply. rTorrent uses it for every command-level
// error (unknown method, unknown info-hash, malformed argument), so callers
// that want to distinguish "torrent is gone" from "the endpoint is down" match
// on this type rather than on the transport error.
type Fault struct {
	// Code is int64 rather than int because that is what int64Value hands back
	// and rTorrent's fault codes are arbitrary integers: narrowing here would
	// silently truncate on the 32-bit ARM builds the release matrix ships.
	Code    int64
	Message string
}

func (f *Fault) Error() string {
	return fmt.Sprintf("rtorrent rpc fault %d: %s", f.Code, f.Message)
}

// arg is anything encodeValue accepts as a call argument. Kept as an explicit
// interface list rather than `any` so an unencodable type is a compile-time
// concern at the call site, not a runtime fault.
type arg interface{}

// blob wraps raw bytes that must be sent as an XML-RPC <base64> parameter.
// load.raw_start takes the .torrent file this way.
type blob []byte

// encodeMethodCall renders an XML-RPC <methodCall> document.
func encodeMethodCall(method string, args ...arg) ([]byte, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><methodCall><methodName>`)
	if err := writeEscaped(&b, method); err != nil {
		return nil, err
	}
	b.WriteString(`</methodName><params>`)
	for _, a := range args {
		b.WriteString(`<param>`)
		if err := encodeValue(&b, a); err != nil {
			return nil, err
		}
		b.WriteString(`</param>`)
	}
	b.WriteString(`</params></methodCall>`)
	return []byte(b.String()), nil
}

func encodeValue(b *strings.Builder, a arg) error {
	switch v := a.(type) {
	case string:
		b.WriteString(`<value><string>`)
		if err := writeEscaped(b, v); err != nil {
			return err
		}
		b.WriteString(`</string></value>`)
	case int:
		// <i4> is core XML-RPC; <i8> is an xmlrpc-c extension. rTorrent's own
		// library accepts both, but anything else that might sit in front of it
		// (a proxy, a test double, another XML-RPC implementation) is only
		// guaranteed the core tags, so an int that fits in 32 bits goes out as
		// <i4>. The decode side accepts <int>, <i4> and <i8> either way.
		if v >= math.MinInt32 && v <= math.MaxInt32 {
			b.WriteString(`<value><i4>` + strconv.Itoa(v) + `</i4></value>`)
		} else {
			b.WriteString(`<value><i8>` + strconv.Itoa(v) + `</i8></value>`)
		}
	case int64:
		if v >= math.MinInt32 && v <= math.MaxInt32 {
			b.WriteString(`<value><i4>` + strconv.FormatInt(v, 10) + `</i4></value>`)
		} else {
			b.WriteString(`<value><i8>` + strconv.FormatInt(v, 10) + `</i8></value>`)
		}
	case bool:
		bit := "0"
		if v {
			bit = "1"
		}
		b.WriteString(`<value><boolean>` + bit + `</boolean></value>`)
	case blob:
		b.WriteString(`<value><base64>` + base64.StdEncoding.EncodeToString(v) + `</base64></value>`)
	default:
		return fmt.Errorf("rtorrent: cannot encode XML-RPC argument of type %T", a)
	}
	return nil
}

// writeEscaped writes s as XML character data with the five predefined
// entities escaped. Torrent names and labels are attacker-influenced (they come
// from an indexer), so this must never be skipped.
func writeEscaped(b *strings.Builder, s string) error {
	return xml.EscapeText(stringWriter{b}, []byte(s))
}

// stringWriter adapts a *strings.Builder to io.Writer for xml.EscapeText.
type stringWriter struct{ b *strings.Builder }

func (w stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

// xmlResponse mirrors the <methodResponse> document shape.
type xmlResponse struct {
	XMLName xml.Name  `xml:"methodResponse"`
	Params  []xmlItem `xml:"params>param"`
	Fault   *xmlItem  `xml:"fault"`
}

type xmlItem struct {
	Value xmlValue `xml:"value"`
}

// xmlValue is one XML-RPC <value>. Every type tag rTorrent emits gets its own
// optional field; a nil pointer means "the reply did not use this type". An
// untyped <value>text</value> (legal, and what rTorrent emits for some string
// results) lands in Text.
type xmlValue struct {
	Text    string     `xml:",chardata"`
	String  *string    `xml:"string"`
	Int     *string    `xml:"int"`
	I4      *string    `xml:"i4"`
	I8      *string    `xml:"i8"`
	Boolean *string    `xml:"boolean"`
	Double  *string    `xml:"double"`
	Base64  *string    `xml:"base64"`
	Array   *xmlArray  `xml:"array"`
	Struct  *xmlStruct `xml:"struct"`
}

type xmlArray struct {
	Values []xmlValue `xml:"data>value"`
}

type xmlStruct struct {
	Members []xmlMember `xml:"member"`
}

type xmlMember struct {
	Name  string   `xml:"name"`
	Value xmlValue `xml:"value"`
}

// decodeMethodResponse parses an XML-RPC reply. A <fault> reply is returned as
// a *Fault error rather than a value.
func decodeMethodResponse(body []byte) (*xmlValue, error) {
	var resp xmlResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode XML-RPC response: %w", err)
	}
	if resp.Fault != nil {
		return nil, faultFrom(&resp.Fault.Value)
	}
	if len(resp.Params) == 0 {
		return nil, fmt.Errorf("XML-RPC response carried no parameters")
	}
	return &resp.Params[0].Value, nil
}

// faultFrom turns the faultCode/faultString struct into a *Fault. A reply that
// claims to be a fault but carries no recognisable struct still produces a
// non-nil error — silently returning nil would let a failed command look like
// a success.
func faultFrom(v *xmlValue) error {
	f := &Fault{Code: -1, Message: "unspecified rTorrent fault"}
	if v == nil || v.Struct == nil {
		return f
	}
	for _, m := range v.Struct.Members {
		switch m.Name {
		case "faultCode":
			if n, err := m.Value.int64Value(); err == nil {
				f.Code = n
			}
		case "faultString":
			f.Message = m.Value.stringValue()
		}
	}
	return f
}

// stringValue renders a value as a string regardless of which type tag carried
// it. rTorrent is inconsistent about tagging — d.name= comes back as <string>
// but some builds emit a bare <value>, and multicall rows mix the two.
func (v *xmlValue) stringValue() string {
	switch {
	case v == nil:
		return ""
	case v.String != nil:
		return *v.String
	case v.Base64 != nil:
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*v.Base64))
		if err != nil {
			return ""
		}
		return string(decoded)
	case v.I8 != nil:
		return strings.TrimSpace(*v.I8)
	case v.Int != nil:
		return strings.TrimSpace(*v.Int)
	case v.I4 != nil:
		return strings.TrimSpace(*v.I4)
	case v.Boolean != nil:
		return strings.TrimSpace(*v.Boolean)
	case v.Double != nil:
		return strings.TrimSpace(*v.Double)
	default:
		return strings.TrimSpace(v.Text)
	}
}

// int64Value renders a value as an int64. Doubles are truncated; a string that
// happens to hold digits is accepted because rTorrent's <value> passthrough
// drops the type tag on some builds.
func (v *xmlValue) int64Value() (int64, error) {
	if v == nil {
		return 0, fmt.Errorf("nil XML-RPC value")
	}
	if v.Double != nil {
		f, err := strconv.ParseFloat(strings.TrimSpace(*v.Double), 64)
		if err != nil {
			return 0, fmt.Errorf("parse XML-RPC double: %w", err)
		}
		return int64(f), nil
	}
	s := v.stringValue()
	if s == "" {
		return 0, fmt.Errorf("empty XML-RPC value where a number was expected")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse XML-RPC integer %q: %w", s, err)
	}
	return n, nil
}

// boolValue treats any non-zero integer as true, matching rTorrent's use of
// i8 0/1 for d.complete, d.is_active and friends.
func (v *xmlValue) boolValue() bool {
	n, err := v.int64Value()
	return err == nil && n != 0
}

// rows flattens an array-of-arrays reply (what d.multicall2 and f.multicall
// return) into one slice of column slices. A reply that is not an array yields
// nil so callers see "no torrents" rather than a decode error — rTorrent
// answers an empty view with an empty <array>, and some proxies normalise that
// to an untyped empty value.
//
// dropped counts entries that were not themselves arrays. Callers log it rather
// than ignoring it: a shape change that turns every row into a non-array makes
// the whole session look empty, and an empty poll routes into
// blockStaleImportFailures, which terminally blocks any download sitting in
// import_failed. A silent drop there costs the user their downloads with no
// trace of why.
func (v *xmlValue) rows() (out [][]xmlValue, dropped int) {
	if v == nil || v.Array == nil {
		return nil, 0
	}
	out = make([][]xmlValue, 0, len(v.Array.Values))
	for i := range v.Array.Values {
		row := v.Array.Values[i]
		if row.Array == nil {
			dropped++
			continue
		}
		out = append(out, row.Array.Values)
	}
	return out, dropped
}

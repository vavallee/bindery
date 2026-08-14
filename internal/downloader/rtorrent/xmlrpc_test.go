package rtorrent

import (
	"errors"
	"strings"
	"testing"
)

// The fixtures below are shaped like real rTorrent 0.9.x replies as seen on the
// wire behind ruTorrent's /plugins/rpc/rpc.php: XML declaration, no namespaces,
// i8 for every numeric, and array-of-array rows for the multicalls.
const (
	versionResponse = `<?xml version="1.0" encoding="UTF-8"?>
<methodResponse>
<params><param><value><string>0.9.8</string></value></param></params>
</methodResponse>`

	faultResponse = `<?xml version="1.0" encoding="UTF-8"?>
<methodResponse>
<fault><value><struct>
<member><name>faultCode</name><value><i4>-501</i4></value></member>
<member><name>faultString</name><value><string>Could not find info-hash.</string></value></member>
</struct></value></fault>
</methodResponse>`

	multicallResponse = `<?xml version="1.0" encoding="UTF-8"?>
<methodResponse>
<params><param><value><array><data>
<value><array><data>
<value><string>The Hobbit</string></value>
<value><string>2B3C4D5E6F708192A3B4C5D6E7F8091A2B3C4D5E</string></value>
<value><string>/home/user/downloads/The Hobbit</string></value>
<value><string>/home/user/downloads/The Hobbit</string></value>
<value><string>sci%20fi</string></value>
<value><i8>1048576</i8></value>
<value><i8>262144</i8></value>
<value><i8>131072</i8></value>
<value><i8>0</i8></value>
<value><i8>1</i8></value>
<value><i8>1</i8></value>
<value><string></string></value>
</data></array></value>
<value><array><data>
<value><string>Dune</string></value>
<value><string>AABBCCDDEEFF00112233445566778899AABBCCDD</string></value>
<value><string>/home/user/downloads/dune.epub</string></value>
<value><string>/home/user/downloads</string></value>
<value><string>books</string></value>
<value><i8>2048</i8></value>
<value><i8>0</i8></value>
<value><i8>0</i8></value>
<value><i8>1</i8></value>
<value><i8>0</i8></value>
<value><i8>1</i8></value>
<value><string>Tracker: [Failure reason "Unregistered torrent"]</string></value>
</data></array></value>
</data></array></value></param></params>
</methodResponse>`

	// An rTorrent view with no torrents. This is the shape that made an
	// array-only decoder look like a decode failure rather than "nothing here".
	emptyMulticallResponse = `<?xml version="1.0"?>
<methodResponse><params><param><value><array><data></data></array></value></param></params></methodResponse>`
)

func TestEncodeMethodCall(t *testing.T) {
	t.Run("string int bool", func(t *testing.T) {
		got, err := encodeMethodCall("d.custom1.set", "ABC", 7, true)
		if err != nil {
			t.Fatalf("encodeMethodCall: %v", err)
		}
		want := `<?xml version="1.0"?><methodCall><methodName>d.custom1.set</methodName><params>` +
			`<param><value><string>ABC</string></value></param>` +
			`<param><value><i4>7</i4></value></param>` +
			`<param><value><boolean>1</boolean></value></param>` +
			`</params></methodCall>`
		if string(got) != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("base64 blob", func(t *testing.T) {
		got, err := encodeMethodCall("load.raw_start", "", blob("hi"))
		if err != nil {
			t.Fatalf("encodeMethodCall: %v", err)
		}
		// "hi" base64-encodes to "aGk=".
		if !strings.Contains(string(got), `<value><base64>aGk=</base64></value>`) {
			t.Fatalf("torrent bytes not base64-encoded: %s", got)
		}
	})

	// Torrent names and labels come from an indexer, so an unescaped payload
	// would let a release title break out of the XML document.
	t.Run("escapes markup in arguments", func(t *testing.T) {
		got, err := encodeMethodCall("load.start", "", `d.custom1.set=<evil>&"x"`)
		if err != nil {
			t.Fatalf("encodeMethodCall: %v", err)
		}
		if strings.Contains(string(got), "<evil>") {
			t.Fatalf("argument markup was not escaped: %s", got)
		}
		if !strings.Contains(string(got), "&lt;evil&gt;&amp;") {
			t.Fatalf("expected escaped entities, got: %s", got)
		}
	})

	t.Run("rejects an unencodable argument", func(t *testing.T) {
		if _, err := encodeMethodCall("d.erase", 1.5); err == nil {
			t.Fatal("expected a float argument to be rejected")
		}
	})
}

func TestDecodeMethodResponse_Scalar(t *testing.T) {
	v, err := decodeMethodResponse([]byte(versionResponse))
	if err != nil {
		t.Fatalf("decodeMethodResponse: %v", err)
	}
	if got := v.stringValue(); got != "0.9.8" {
		t.Fatalf("version: got %q, want %q", got, "0.9.8")
	}
}

func TestDecodeMethodResponse_Fault(t *testing.T) {
	_, err := decodeMethodResponse([]byte(faultResponse))
	if err == nil {
		t.Fatal("expected a fault reply to decode as an error")
	}
	var fault *Fault
	if !errors.As(err, &fault) {
		t.Fatalf("expected *Fault, got %T: %v", err, err)
	}
	if fault.Code != -501 {
		t.Errorf("fault code: got %d, want -501", fault.Code)
	}
	if fault.Message != "Could not find info-hash." {
		t.Errorf("fault message: got %q", fault.Message)
	}
	if !strings.Contains(fault.Error(), "-501") {
		t.Errorf("Error() should name the code, got %q", fault.Error())
	}
}

// A reply that announces a fault but carries no struct must still be an error.
// Returning nil there would make a rejected command read as a success.
func TestDecodeMethodResponse_FaultWithoutStruct(t *testing.T) {
	_, err := decodeMethodResponse([]byte(`<methodResponse><fault><value/></fault></methodResponse>`))
	if err == nil {
		t.Fatal("expected an error for a structless fault")
	}
	var fault *Fault
	if !errors.As(err, &fault) {
		t.Fatalf("expected *Fault, got %T", err)
	}
}

func TestDecodeMethodResponse_NotXMLRPC(t *testing.T) {
	// What a user gets when the URL path points at ruTorrent's web root.
	_, err := decodeMethodResponse([]byte("<html><body>ruTorrent</body></html>"))
	if err == nil {
		t.Fatal("expected an error for a non-XML-RPC document")
	}
	var fault *Fault
	if errors.As(err, &fault) {
		t.Fatal("an HTML page must not be reported as an rTorrent fault")
	}
}

func TestDecodeMethodResponse_NoParams(t *testing.T) {
	if _, err := decodeMethodResponse([]byte(`<methodResponse><params></params></methodResponse>`)); err == nil {
		t.Fatal("expected an error when the reply carries no params")
	}
}

func TestRows(t *testing.T) {
	v, err := decodeMethodResponse([]byte(multicallResponse))
	if err != nil {
		t.Fatalf("decodeMethodResponse: %v", err)
	}
	rows, dropped := v.rows()
	if dropped != 0 {
		t.Fatalf("a well-formed multicall reply should drop nothing, got %d", dropped)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}
	if len(rows[0]) != 12 {
		t.Fatalf("row 0 columns: got %d, want 12", len(rows[0]))
	}
	if got := rows[0][0].stringValue(); got != "The Hobbit" {
		t.Errorf("row 0 name: got %q", got)
	}
	if got, err := rows[0][5].int64Value(); err != nil || got != 1048576 {
		t.Errorf("row 0 size: got %d (%v)", got, err)
	}

	empty, err := decodeMethodResponse([]byte(emptyMulticallResponse))
	if err != nil {
		t.Fatalf("decode empty multicall: %v", err)
	}
	if got, dropped := empty.rows(); len(got) != 0 || dropped != 0 {
		t.Fatalf("empty view should decode to zero rows and drop nothing, got %d rows / %d dropped", len(got), dropped)
	}

	// A reply whose entries are not themselves arrays is the shape-drift case:
	// the rows are unusable, and the count is what the caller logs so the
	// operator is not left with a silently empty poll.
	skewed, err := decodeMethodResponse([]byte(`<methodResponse><params><param><value><array><data><value><string>not-a-row</string></value><value><string>nor-this</string></value></data></array></value></param></params></methodResponse>`))
	if err != nil {
		t.Fatalf("decode skewed multicall: %v", err)
	}
	if got, dropped := skewed.rows(); len(got) != 0 || dropped != 2 {
		t.Fatalf("non-array entries should be reported as dropped, got %d rows / %d dropped", len(got), dropped)
	}
}

func TestValueConversions(t *testing.T) {
	t.Run("int64 from every numeric tag", func(t *testing.T) {
		for _, doc := range []string{
			`<methodResponse><params><param><value><i8>42</i8></value></param></params></methodResponse>`,
			`<methodResponse><params><param><value><i4>42</i4></value></param></params></methodResponse>`,
			`<methodResponse><params><param><value><int>42</int></value></param></params></methodResponse>`,
			`<methodResponse><params><param><value><double>42.7</double></value></param></params></methodResponse>`,
			// Untagged <value> — legal XML-RPC and emitted by some SCGI proxies.
			`<methodResponse><params><param><value>42</value></param></params></methodResponse>`,
		} {
			v, err := decodeMethodResponse([]byte(doc))
			if err != nil {
				t.Fatalf("decode %s: %v", doc, err)
			}
			got, err := v.int64Value()
			if err != nil || got != 42 {
				t.Fatalf("%s: got %d (%v), want 42", doc, got, err)
			}
		}
	})

	t.Run("int64 rejects non-numeric", func(t *testing.T) {
		v, err := decodeMethodResponse([]byte(`<methodResponse><params><param><value><string>nope</string></value></param></params></methodResponse>`))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if _, err := v.int64Value(); err == nil {
			t.Fatal("expected a non-numeric string to fail int64Value")
		}
	})

	t.Run("bool is non-zero", func(t *testing.T) {
		cases := map[string]bool{
			`<methodResponse><params><param><value><i8>1</i8></value></param></params></methodResponse>`: true,
			`<methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`: false,
			// An unreadable value must not read as true.
			`<methodResponse><params><param><value><string></string></value></param></params></methodResponse>`: false,
		}
		for doc, want := range cases {
			v, err := decodeMethodResponse([]byte(doc))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := v.boolValue(); got != want {
				t.Errorf("%s: got %v, want %v", doc, got, want)
			}
		}
	})

	t.Run("base64 value decodes to its bytes", func(t *testing.T) {
		v, err := decodeMethodResponse([]byte(`<methodResponse><params><param><value><base64>aGk=</base64></value></param></params></methodResponse>`))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := v.stringValue(); got != "hi" {
			t.Fatalf("got %q, want %q", got, "hi")
		}
	})
}

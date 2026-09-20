package simulate

import (
	"bytes"
	"io"
)

// newTrimReader returns a reader over data, tolerating a UTF-8 BOM that some
// editors prepend and that json.Decode rejects with an opaque error.
func newTrimReader(data []byte) io.Reader {
	return bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
}

// Package termtext renders bounded JSON for frontend terminal displays.
package termtext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const MaxBytes = 64 << 10

const omittedJSON = `"Usage detail omitted: display limit is 65536 bytes."`

// JSON preserves JSON number spelling and escapes terminal controls. One byte
// of the output budget is reserved for a CLI newline. Oversized data is omitted
// rather than truncated into partial JSON or a partial escape sequence.
func JSON(raw json.RawMessage) string {
	if len(raw) > MaxBytes-1 {
		return omittedJSON
	}
	if len(raw) == 0 {
		return `"No usage detail was returned."`
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		quoted, _ := json.Marshal(string(raw))
		formatted.Reset()
		formatted.Write(quoted)
	}
	if formatted.Len() > MaxBytes-1 {
		return omittedJSON
	}
	var safe strings.Builder
	for _, value := range formatted.String() {
		if unicode.IsControl(value) && value != '\n' && value != '\t' {
			fmt.Fprintf(&safe, `\u%04x`, value)
		} else {
			safe.WriteRune(value)
		}
		if safe.Len() > MaxBytes-1 {
			return omittedJSON
		}
	}
	return ansi.Strip(safe.String())
}

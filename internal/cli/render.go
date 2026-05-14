package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// render writes data to w in the format selected by --output. For text it
// falls back to a YAML rendering, which is human-readable and consistent
// with the JSON/YAML branches.
func render(w io.Writer, data any) error {
	switch rootOutput {
	case OutputJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	case OutputYAML, OutputText, "":
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer enc.Close()
		return enc.Encode(data)
	default:
		return fmt.Errorf("unknown output format: %s", rootOutput)
	}
}

// renderStringer is a specialisation for types with a meaningful .String(),
// used for the Plan output where text mode prints the human form.
type stringer interface {
	String() string
}

func renderStringer(w io.Writer, data stringer) error {
	if rootOutput == OutputText || rootOutput == "" {
		_, err := fmt.Fprint(w, data.String())
		return err
	}
	return render(w, data)
}

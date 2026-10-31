package hooks

import (
	"bytes"
	"encoding/json"
	"text/template"
)

// bodyTemplate parses a webhook's body: a Go text/template over the payload
// as its JSON reads (.event, .job.title, .items), with json to write a value
// as a JSON literal inside a JSON body.
func bodyTemplate(src string) (*template.Template, error) {
	return template.New("body").Funcs(template.FuncMap{"json": jsonLiteral}).Parse(src)
}

func jsonLiteral(v any) (string, error) {
	raw, err := json.Marshal(v)
	return string(raw), err
}

// renderBody is the request body of a webhook whose body is src.
func renderBody(src string, payload Payload) ([]byte, error) {
	t, err := bodyTemplate(src)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

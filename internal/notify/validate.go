package notify

import (
	"slices"
	"strings"
	"unicode"

	"github.com/master2sid2/cert-alerter/internal/validate"
)

// Normalize trims values, drops unknown fields, fills defaults and checks
// the generic rules of fields. It returns field name -> error code.
func Normalize(fields []Field, in Config) (Config, map[string]string) {
	out := Config{}
	errs := map[string]string{}
	for _, f := range fields {
		v := in[f.Name]
		if f.Kind != KindTextarea && f.Kind != KindSecret {
			v = strings.TrimSpace(v)
		}
		if v == "" {
			v = f.Default
		}
		switch f.Kind {
		case KindBool:
			if v == "true" {
				out[f.Name] = "true"
			} else {
				out[f.Name] = "false"
			}
			continue
		case KindSecret:
			v = strings.TrimSpace(v)
		}
		switch {
		case v == "":
			if f.Required {
				errs[f.Name] = validate.Required
			}
		case len(v) > f.maxLength():
			errs[f.Name] = validate.TooLong
		case strings.ContainsFunc(v, func(r rune) bool {
			if !unicode.IsControl(r) {
				return false
			}
			// Multi-line fields may hold line breaks and tabs.
			return f.Kind != KindTextarea || (r != '\n' && r != '\r' && r != '\t')
		}):
			errs[f.Name] = validate.Invalid
		case f.Kind == KindURL && !ValidURL(v):
			errs[f.Name] = validate.Invalid
		case f.Kind == KindSelect && !slices.ContainsFunc(f.Options, func(o Option) bool { return o.Value == v }):
			errs[f.Name] = validate.Invalid
		}
		out[f.Name] = v
	}
	return out, errs
}

// Secrets returns the names of the secret fields.
func Secrets(fields []Field) []string {
	var out []string
	for _, f := range fields {
		if f.Kind == KindSecret {
			out = append(out, f.Name)
		}
	}
	return out
}

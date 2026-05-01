package engine

import (
	"strings"

	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// evaluateWhen returns true if all when expressions are satisfied. An empty
// list is satisfied by definition.
func evaluateWhen(exprs []tektontypes.WhenExpression, ctx resolver.Context) (bool, string, error) {
	for _, w := range exprs {
		input, err := resolver.Substitute(w.Input, ctx)
		if err != nil {
			return false, "", err
		}
		match := false
		for _, v := range w.Values {
			vv, err := resolver.Substitute(v, ctx)
			if err != nil {
				return false, "", err
			}
			if input == vv {
				match = true
				break
			}
		}
		op := strings.ToLower(w.Operator)
		switch op {
		case "in":
			if !match {
				return false, formatWhen(w, input), nil
			}
		case "notin":
			if match {
				return false, formatWhen(w, input), nil
			}
		}
	}
	return true, "", nil
}

func formatWhen(w tektontypes.WhenExpression, input string) string {
	return "input=" + input + " " + w.Operator + " " + strings.Join(w.Values, ",")
}

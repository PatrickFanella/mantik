package experimentplan

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func ReadRecipe(input io.Reader) (Recipe, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
	if err != nil {
		return Recipe{}, err
	}
	if len(raw) > 1<<20 {
		return Recipe{}, errors.New("recipe exceeds 1 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var r Recipe
	if err := decoder.Decode(&r); err != nil {
		return Recipe{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Recipe{}, errors.New("expected one recipe JSON value")
	}
	return r, r.Validate()
}
